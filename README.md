# github-fetch

# Usage

```
$ github-fetch --help
Fetches GitHub repos into a local directory

Usage: github-fetch [OPTIONS]

OPTIONS:
  -concurrent int
    	Number of concurrent processes to run (default 10)
  -dir string
    	Directory to fetch the repos into
  -owner string
    	Owner to fetch (default "mgbelisle")
  -repos string
    	String separated list of repos to fetch. Empty (default) means all repos in --owner
  -verbose
    	Verbose logging
```

# Example

```
$ github-fetch --owner mgbelisle --dir ~/github
$ 
```

# Installation

```
go install github.com/mgbelisle/github-fetch
```
