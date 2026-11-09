package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
)

var usagePrefix = fmt.Sprintf(`Fetches GitHub repos into a local directory

Usage: %s [OPTIONS]

OPTIONS:
`, os.Args[0])

var (
	concurrentFlag = flag.Int("concurrent", 10, "Number of concurrent processes to run")
	dirFlag        = flag.String("dir", "", "Directory to fetch the repos into")
	ownerFlag      = flag.String("owner", "mgbelisle", "Owner to fetch")
	reposFlag      = flag.String("repos", "", "String separated list of repos to fetch. Empty (default) means all repos in --owner")
	verboseFlag    = flag.Bool("verbose", false, "Verbose logging")

	githubToken = os.Getenv("GITHUB_TOKEN")
)

func main() {
	// Flag setup
	flag.Usage = func() {
		fmt.Fprint(os.Stdout, usagePrefix)
		flag.PrintDefaults()
	}
	flag.Parse()
	local(*ownerFlag)

	if *dirFlag == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			panic(err)
		}
		*dirFlag = filepath.Join(home, "github")
	}

	// Validate GITHUB_TOKEN
	request, _ := http.NewRequest("GET", "https://api.github.com/user", nil)
	request.Header.Set("Authorization", "token "+githubToken)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		panic(err)
	}
	respObj := struct {
		Login string `json:"login"`
	}{}
	json.NewDecoder(response.Body).Decode(&respObj)
	if respObj.Login == "" {
		panic("Invalid GITHUB_TOKEN")
	}

	errs := make(chan error)
	concurrent := make(chan struct{}, *concurrentFlag)

	// List repos
	var repos []string
	if fields := strings.Fields(*reposFlag); 0 < len(fields) {
		for _, repo := range fields {
			local(repo)
		}
		repos = fields
	} else {
		for i := 1; ; i++ {
			request, _ := http.NewRequest("GET", fmt.Sprintf("https://api.github.com/orgs/%s/repos?page=%d&per_page=100", url.PathEscape(*ownerFlag), i), nil)
			request.Header.Set("Authorization", "token "+githubToken)
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				panic(err)
			}
			if response.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(response.Body)
				panic(fmt.Errorf("%s %d\n%s", request.URL, response.StatusCode, body))
			}
			responseObj := []struct {
				Name     string `json:"name"`
				Archived bool   `json:"archived"`
			}{}
			if err := json.NewDecoder(response.Body).Decode(&responseObj); err != nil {
				panic(err)
			}
			if len(responseObj) == 0 {
				break
			}
			for _, obj := range responseObj {
				local(obj.Name)
				if obj.Archived {
					if err := os.RemoveAll(filepath.Join(*dirFlag, *ownerFlag, obj.Name)); err != nil {
						panic(err)
					}
				} else {
					repos = append(repos, obj.Name)
				}
			}
		}
	}

	// Fetch all repos
	syscall.Umask(0) // No umask, users own these files
	fetchWg := sync.WaitGroup{}
	for _, repo := range repos {
		fetchWg.Add(1)
		go func(repo string) {
			concurrent <- struct{}{}
			if err := fetch(repo); err != nil {
				errs <- err
			}
			<-concurrent
			fetchWg.Add(-1)
		}(repo)
	}

	go func() {
		fetchWg.Wait()
		close(errs)
	}()

	exit := 0
	errLog := log.New(os.Stderr, os.Args[0]+": ", 0)
	for err := range errs {
		errLog.Print(err)
		exit = 1
	}
	os.Exit(exit)
}

type logWriter struct{ *log.Logger }

func (w logWriter) Write(b []byte) (int, error) {
	w.Printf("%s", b)
	return len(b), nil
}

func git(args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	config := [][2]string{
		{"gc.auto", "0"},
		{"maintenance.auto", "false"},
		{"fetch.writeCommitGraph", "false"},
		{"core.multiPackIndex", "false"},
		{"core.commitGraph", "false"},
		{"http.extraheader", "Authorization: bearer " + githubToken},
	}
	cmd.Env = append(os.Environ(), fmt.Sprintf("GIT_CONFIG_COUNT=%d", len(config)))
	for i, kv := range config {
		cmd.Env = append(cmd.Env,
			fmt.Sprintf("GIT_CONFIG_KEY_%d=%s", i, kv[0]),
			fmt.Sprintf("GIT_CONFIG_VALUE_%d=%s", i, kv[1]),
		)
	}
	prefix := strings.Join(append([]string{"git"}, args...), " ") + ": "
	var stdout bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = logWriter{log.New(os.Stderr, prefix, 0)}
	if *verboseFlag {
		logWriter := logWriter{log.New(os.Stdout, prefix, 0)}
		logWriter.Print("")
		cmd.Stdout = io.MultiWriter(&stdout, logWriter)
	}
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%s%v", prefix, err)
	}
	return stdout.String(), nil
}

func gitHead(dir string) (string, error) {
	head, err := git("-C", dir, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(head), nil
}

func local(name string) {
	if !filepath.IsLocal(name) {
		panic(fmt.Errorf("invalid path %q", name))
	}
}

func fetch(repo string) error {
	dir := filepath.Join(*dirFlag, *ownerFlag, repo)
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		// Repo does not exist, initialize it
		if err := os.MkdirAll(dir, 0777); err != nil {
			return err
		}
		if _, err := git("-C", dir, "init"); err != nil {
			return err
		}
	}

	// Find remote HEAD
	url := fmt.Sprintf("https://github.com/%s/%s.git", url.PathEscape(*ownerFlag), url.PathEscape(repo))
	remote, err := git("-C", dir, "ls-remote", url, "HEAD")
	if err != nil {
		return err
	}
	fields := strings.Fields(remote)
	if len(fields) == 0 {
		return nil // Remote is empty, skip it
	}
	remoteHead := fields[0]

	// Skip the fetch if we already have the remote HEAD checked out
	if local, err := gitHead(dir); err == nil && local == remoteHead {
		return nil
	}

	// Fetch the remote HEAD
	if _, err := git("-C", dir, "fetch", "--depth", "1", "--no-tags", url, "HEAD"); err != nil {
		return err
	}
	if _, err := git("-C", dir, "checkout", "FETCH_HEAD"); err != nil {
		return err
	}
	return nil
}
