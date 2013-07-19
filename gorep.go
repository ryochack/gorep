package main

import (
	"bytes"
	term "code.google.com/p/go.crypto/ssh/terminal"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"regexp"
	"runtime"
	"strings"
	"syscall"
)

const version = "0.1"

type fileMode int32

const (
	FMODE_DIR fileMode = iota
	FMODE_FILE
	FMODE_SYMLINK
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

func usage() {
	fmt.Fprintf(os.Stderr, `gorep is find and grep tool.

Version: %s

Usage:

    gorep [options] pattern [path]

The options are:

    -g    : enable grep
    -V    : print gorep version
`, version)
	os.Exit(-1)
}

var semaphore chan int

const maxNumOfFileOpen = 10

var isColor bool

const (
	DIR_COLOR     = "\x1b[36m"
	FILE_COLOR    = "\x1b[34m"
	SYMLINK_COLOR = "\x1b[35m"
	GREP_COLOR    = "\x1b[32m"
	HIT_COLOR     = "\x1b[32m"
	NORM_COLOR    = "\x1b[39m"
	BOLD_DECO     = "\x1b[1m"
	NORM_DECO     = "\x1b[0m"
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
	requireVersion := flag.Bool("V", false, "show version.")
	flag.Parse()

	if *requireVersion {
		fmt.Printf("version: %s\n", version)
		os.Exit(0)
	}

	if flag.NArg() < 1 {
		usage()
	}

	pattern := flag.Arg(0)
	fpath := "."
	if flag.NArg() >= 2 {
		fpath = strings.TrimRight(flag.Arg(1), "/")
	}

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
		os.Exit(-1)
	}
	return &gorep{requireRecursive, requireFile, requireGrep, compiledPattern}
}

func (this gorep) showReport(chNotify <-chan report) {
	var accentPattern string
	var dirPattern string
	var filePattern string
	var symlinkPattern string
	var grepPattern string
	if isColor {
		accentPattern  = BOLD_DECO + HIT_COLOR + "$0" + NORM_COLOR + NORM_DECO
		dirPattern     = DIR_COLOR + "[Dir ]" + NORM_COLOR
		filePattern    = FILE_COLOR + "[File]" + NORM_COLOR
		symlinkPattern = SYMLINK_COLOR + "[SymL]" + NORM_COLOR
		grepPattern    = GREP_COLOR + "[Grep]" + NORM_COLOR
	} else {
		accentPattern  = "$0"
		dirPattern     = "[Dir ]"
		filePattern    = "[File]"
		symlinkPattern = "[SymL]"
		grepPattern    = "[Grep]"
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
		case FMODE_SYMLINK:
			accentPath := this.pattern.ReplaceAllString(repo.fpath, accentPattern)
			fmt.Printf("%s %s\n", symlinkPattern, accentPath)
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
			case FMODE_SYMLINK:
				if this.bFind && this.pattern.MatchString(path.Base(repo.fpath)) {
					chNotify <- repo
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
		fmt.Fprintf(os.Stderr, "dive error: %v\n", err)
		os.Exit(-1)
	}

	const ignoreFlag = os.ModeDir | os.ModeAppend | os.ModeExclusive | os.ModeTemporary |
						os.ModeSymlink | os.ModeDevice | os.ModeNamedPipe | os.ModeSocket |
						os.ModeSetuid | os.ModeSetgid | os.ModeCharDevice | os.ModeSticky

	for _, finfo := range list {
		var myFmode fileMode
		mode := finfo.Mode()
		switch true {
		case mode & os.ModeDir != 0:
			myFmode = FMODE_DIR
		case mode & os.ModeSymlink != 0:
			myFmode = FMODE_SYMLINK
		case mode & ignoreFlag == 0:
			myFmode = FMODE_FILE
		default:
			continue
		}
		chRelay <- report{false, myFmode, dir + "/" + finfo.Name(), ""}
	}
}

// Charactor code 0x00 - 0x08 is control code (ASCII)
func identifyBinary(buf []byte) bool {
	var b []byte
	if len(buf) > 256 {
		b = buf[:256]
	} else {
		b = buf
	}
	if bytes.IndexFunc(b, func(r rune) bool { return r < 0x09 }) != -1 {
		return true
	}
	return false
}

func (this gorep) grep(fpath string, chRelay chan<- report) {
	defer func() {
		<-semaphore
		chRelay <- report{true, FMODE_LINE, "", ""}
	}()

	semaphore <- 1

	file, err := os.Open(fpath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "grep open error: %v\n", err)
		return
	}
	defer file.Close()

	fi, err := file.Stat()
	if err != nil {
		fmt.Fprintf(os.Stderr, "grep stat error: %v\n", err)
		return
	}

	mem, err := syscall.Mmap(int(file.Fd()), 0, int(fi.Size()),
							syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		//fmt.Fprintf(os.Stderr, "grep mmap error: %v\n", err)
		return
	}
	defer syscall.Munmap(mem)

	if identifyBinary(mem) {
		return
	}

	lineNumber := 0
	var line []byte

	for m := mem; len(m) > 0;  {
		index := bytes.IndexRune(m, rune('\n'))
		if index != -1 {
			line = m[:index]
			m = m[len(line)+1:]	/* +1 is to skip '\n' */
		} else {
			line = m
			m = m[len(line):]
		}

		lineNumber++
		strline := string(line)

		if this.pattern.MatchString(strline) {
			formatline := fmt.Sprintf("%d: %s", lineNumber, strline)
			chRelay <- report{false, FMODE_LINE, fpath, formatline}
		}
	}
}
