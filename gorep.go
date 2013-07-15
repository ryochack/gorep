package main

import (
	"bufio"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"regexp"
	"runtime"
)

type fileMode int32

const (
	FMODE_DIR fileMode = iota
	FMODE_FILE
	FMODE_LINE
	FMODE_INVALID
)

type report struct {
	complete bool
	fmode    fileMode
	fpath    string
	line     string
}

type gorep struct {
	bRecursive bool
	bFind      bool
	bGrep      bool
	pattern    *regexp.Regexp
}

func usage(progName string) {
	fmt.Printf("%s [-g] PATTERN PATH\n", path.Base(progName))
}

var semaphore chan int
const maxNumOfFileOpen = 10

func init() {
	semaphore = make(chan int, maxNumOfFileOpen)
}

func main() {
	cpus := runtime.NumCPU()
	runtime.GOMAXPROCS(cpus)

	/* parse flag */
	requireRecursive := flag.Bool("r", true, "enable recursive search.")
	requireFile := flag.Bool("f", true, "enable file search.")
	requireGrep := flag.Bool("g", false, "enable grep.")
	flag.Parse()

	if flag.NArg() < 2 {
		usage(os.Args[0])
		os.Exit(0)
	}

	pattern := flag.Arg(0)
	fpath := flag.Arg(1)

	fmt.Printf("pattern:%s path:%s -r:%v -f:%v -g:%v\n", pattern, fpath,
		*requireRecursive, *requireFile, *requireGrep)

	/* create gorep */
	c := New(*requireRecursive, *requireFile, *requireGrep, pattern)

	/* make notify channel & start gorep */
	chNotify := c.kick(fpath)

	c.showReport(chNotify)
}

func New(requireRecursive, requireFile, requireGrep bool, pattern string) *gorep {
	compiledPattern, err := regexp.Compile(pattern)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	return &gorep{requireRecursive, requireFile, requireGrep, compiledPattern}
}

func (this gorep) showReport(chNotify <-chan report) {
	const accentPattern = "\x1b[1m\x1b[32m$0\x1b[36m\x1b[0m"
	/* receive notify */
	for repo, ok := <-chNotify; ok; repo, ok = <-chNotify {
		switch repo.fmode {
		case FMODE_DIR:
			accentPath := this.pattern.ReplaceAllString(repo.fpath, accentPattern)
			fmt.Printf("\x1b[36m[Dir ]\x1b[36m %s\n", accentPath)
		case FMODE_FILE:
			accentPath := this.pattern.ReplaceAllString(repo.fpath, accentPattern)
			fmt.Printf("\x1b[34m[File]\x1b[36m %s\n", accentPath)
		case FMODE_LINE:
			accentLine := this.pattern.ReplaceAllString(repo.line, accentPattern)
			fmt.Printf("\x1b[32m[Grep]\x1b[36m %s:%s\n", repo.fpath, accentLine)
		default:
			fmt.Fprintf(os.Stderr, "Illegal filemode (%d)\n", repo.fmode)
		}
	}
}

func (this gorep) kick(fpath string) <-chan report {
	chNotify := make(chan report)

	/* make child channel */
	chRelay := make(chan report, 10)
	nRoutines := 0

	nRoutines++
	go this.dive(fpath, chRelay)

	go func() {
		for nRoutines > 0 {
			repo := <-chRelay

			if repo.complete {
				nRoutines--
				continue
			}

			switch repo.fmode {
			case FMODE_DIR:
				if this.bFind && this.pattern.MatchString(path.Base(repo.fpath)) {
					chNotify <- repo
				}
				if this.bRecursive {
					nRoutines++
					go this.dive(repo.fpath, chRelay)
				}
			case FMODE_FILE:
				if this.bFind && this.pattern.MatchString(path.Base(repo.fpath)) {
					chNotify <- repo
				}
				if this.bGrep {
					nRoutines++
					go this.grep(repo.fpath, chRelay)
				}
			case FMODE_LINE:
				chNotify <- repo
			default:
				fmt.Fprintf(os.Stderr, "Illegal filemode (%d)\n", repo.fmode)
			}
		}
		close(chRelay)
		close(chNotify)
	}()

	return chNotify
}

func (this gorep) dive(dir string, chRelay chan<- report) {
	defer func() {
		chRelay <- report{true, FMODE_DIR, "", ""}
	}()

	/* expand dir */
	list, err := ioutil.ReadDir(dir)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}

	for _, finfo := range list {
		var fmode fileMode
		if finfo.IsDir() {
			fmode = FMODE_DIR
		} else {
			fmode = FMODE_FILE
		}
		chRelay <- report{false, fmode, dir + "/" + finfo.Name(), ""}
	}
}

// Binary check.
// Character code deesn't include 00
func isBinary(buf []byte) bool {
	for _, b := range buf {
		if b == 0 {
			return true
		}
	}
	return false
}


func (this gorep) grep(fpath string, chRelay chan<- report) {
	defer func() {
		chRelay <- report{true, FMODE_LINE, "", ""}
	}()

	semaphore <- 1

	file, err := os.Open(fpath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return
	}
	defer func() {
		file.Close()
		<- semaphore
	}()

	lineNumber := 0
	lineReader := bufio.NewReaderSize(file, 256)

	for {
		line, isPrefix, err := lineReader.ReadLine()
		if err != nil {
			return
		}
		if isBinary(line) {
			return
		}
		lineNumber++
		fullLine := string(line)
		if isPrefix {
			fullLine = fullLine + "@@"
		}
		if this.pattern.MatchString(fullLine) {
			formatline := fmt.Sprintf("%d: %s", lineNumber, fullLine)
			chRelay <- report{false, FMODE_LINE, fpath, formatline}
		}
	}
}
