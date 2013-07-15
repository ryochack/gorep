package main

import (
	"bufio"
	"bytes"
	term "code.google.com/p/go.crypto/ssh/terminal"
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

var isColor bool

const (
	DIR_COLOR  = "\x1b[36m"
	FILE_COLOR = "\x1b[34m"
	GREP_COLOR = "\x1b[32m"
	HIT_COLOR  = "\x1b[32m"
	NORM_COLOR = "\x1b[39m"
	BOLD_DECO  = "\x1b[1m"
	NORM_DECO  = "\x1b[0m"
)

func init() {
	semaphore = make(chan int, maxNumOfFileOpen)
	fd := os.Stdout.Fd()
	isColor = term.IsTerminal(int(fd))
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
	c := newGorep(*requireRecursive, *requireFile, *requireGrep, pattern)

	/* make notify channel & start gorep */
	chNotify := c.kick(fpath)

	c.showReport(chNotify)
}

func newGorep(requireRecursive, requireFile, requireGrep bool, pattern string) *gorep {
	compiledPattern, err := regexp.Compile(pattern)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(1)
	}
	return &gorep{requireRecursive, requireFile, requireGrep, compiledPattern}
}

func (this gorep) showReport(chNotify <-chan report) {
	var accentPattern string
	var dirPattern string
	var filePattern string
	var grepPattern string
	if isColor {
		accentPattern = BOLD_DECO + HIT_COLOR + "$0" + NORM_COLOR + NORM_DECO
		dirPattern = DIR_COLOR + "[Dir ]" + NORM_COLOR
		filePattern = FILE_COLOR + "[File]" + NORM_COLOR
		grepPattern = GREP_COLOR + "[Grep]" + NORM_COLOR
	} else {
		accentPattern = "$0"
		dirPattern = "[Dir ]"
		filePattern = "[File]"
		grepPattern = "[Grep]"
	}

	/* receive notify */
	for repo, ok := <-chNotify; ok; repo, ok = <-chNotify {
		switch repo.fmode {
		case FMODE_DIR:
			accentPath := this.pattern.ReplaceAllString(repo.fpath, accentPattern)
			fmt.Printf("%s %s\n", dirPattern, accentPath)
		case FMODE_FILE:
			accentPath := this.pattern.ReplaceAllString(repo.fpath, accentPattern)
			fmt.Printf("%s %s\n", filePattern, accentPath)
		case FMODE_LINE:
			accentLine := this.pattern.ReplaceAllString(repo.line, accentPattern)
			fmt.Printf("%s %s:%s\n", grepPattern, repo.fpath, accentLine)
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

// Charactor code 0x00 - 0x08 is control code (ASCII)
func isBinary(buf []byte) bool {
	if bytes.IndexFunc(buf, func(r rune) bool { return r < 0x9 }) != -1 {
		return true
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
		<-semaphore
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
