package main

import (
	"bytes"
	"code.google.com/p/go.crypto/ssh/terminal"
	"flag"
	"fmt"
	"io/ioutil"
	"os"
	"path"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"syscall"
)

const version = "0.2.5"

type channelSet struct {
	dir chan string
	file chan string
	symlink chan string
	line chan string
}

type optionSet struct {
	v bool
	g bool
	grep_only bool
	search_binary bool
	ignore string
	hidden bool
	ignorecase bool
}

type searchScope struct {
	dir bool
	file bool
	symlink bool
	grep bool
	binary bool
	hidden bool
}

type gorep struct {
	pattern *regexp.Regexp
	ignorePattern *regexp.Regexp
	scope searchScope
}

var semFopenLimit chan int
const maxNumOfFileOpen = 10

var waitMaps sync.WaitGroup
var waitGreps sync.WaitGroup

const separator = string(os.PathSeparator)

func usage() {
	fmt.Fprintf(os.Stderr, `gorep is find and grep tool.

Version: %s

Usage:

    gorep [options] pattern [path]

The options are:

    -V              : print gorep version
    -g              : enable grep
    -grep-only      : enable grep and disable file search
    -search-binary  : search binary files for matches on grep enable
    -ignore pattern : pattern is ignored
    -hidden         : search hidden files
    -ignorecase     : ignore case distinctions in pattern
`, version)
	os.Exit(-1)
}

func init() {
	semFopenLimit = make(chan int, maxNumOfFileOpen)
}

func verifyColor() bool {
	fd := os.Stdout.Fd()
	isTerm := terminal.IsTerminal(int(fd))
	return isTerm
}

func main() {
	runtime.GOMAXPROCS(runtime.NumCPU())

	var opt optionSet
	flag.BoolVar(&opt.v, "V", false, "show version.")
	flag.BoolVar(&opt.g, "g", false, "enable grep.")
	flag.BoolVar(&opt.search_binary, "search-binary", false, "search binary files for matches on grep enable.")
	flag.BoolVar(&opt.grep_only, "grep-only", false, "enable grep and disable file search.")
	flag.StringVar(&opt.ignore, "ignore", "", "pattern is ignored.")
	flag.BoolVar(&opt.hidden, "hidden", false, "search hidden files.")
	flag.BoolVar(&opt.ignorecase, "ignorecase", false, "ignore case distinctions in pattern.")
	flag.Parse()

	if opt.v {
		fmt.Printf("version: %s\n", version)
		os.Exit(0)
	}

	if flag.NArg() < 1 {
		usage()
	}
	pattern := flag.Arg(0)
	fpath := "."
	if flag.NArg() >= 2 {
		fpath = strings.TrimRight(flag.Arg(1), separator)
	}

	g := newGorep(pattern, &opt)
	chans := g.kick(fpath)

	g.report(chans, verifyColor())
}

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

func (this gorep) report(chans *channelSet, isColor bool) {
	var markMatch string
	var markDir string
	var markFile string
	var markSymlink string
	var markGrep string
	if isColor {
		markMatch   = BOLD_DECO + HIT_COLOR + "$0" + NORM_COLOR + NORM_DECO
		markDir     = DIR_COLOR + "[Dir ]" + NORM_COLOR
		markFile    = FILE_COLOR + "[File]" + NORM_COLOR
		markSymlink = SYMLINK_COLOR + "[SymL]" + NORM_COLOR
		markGrep    = GREP_COLOR + "[Grep]" + NORM_COLOR
	} else {
		markMatch   = "$0"
		markDir     = "[Dir ]"
		markFile    = "[File]"
		markSymlink = "[SymL]"
		markGrep    = "[Grep]"
	}

	var waitReports sync.WaitGroup

	chPrint := make(chan []byte)
	// printer
	go func() {
		for {
			os.Stdout.Write(<-chPrint)
		}
	}()

	reporter := func(mark string, accent string, ch <-chan string) {
		defer waitReports.Done()
		for msg := range ch {
			decoStr := this.pattern.ReplaceAllString(msg, accent)
			chPrint <- []byte(fmt.Sprintf("%s %s\n", mark, decoStr))
		}
	}

	waitReports.Add(4)
	go reporter(markDir    , markMatch, chans.dir)
	go reporter(markFile   , markMatch, chans.file)
	go reporter(markSymlink, markMatch, chans.symlink)
	go reporter(markGrep   , markMatch, chans.line)
	waitReports.Wait()
}

func newGorep(pattern string, opt *optionSet) *gorep {
	base := &gorep{
		pattern: nil,
		ignorePattern: nil,
		scope: searchScope{
			dir: true,
			file: true,
			symlink: true,
			grep: false,
			binary: false,
			hidden: false,
		},
	}

	// config regexp
	if opt.ignorecase {
		pattern = "(?i)" + pattern
	}

	var err error
	base.pattern, err = regexp.Compile(pattern)
	if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		os.Exit(-1)
	}

	if len(opt.ignore) > 0 {
		if opt.ignorecase {
			opt.ignore = "(?i)" + opt.ignore
		}
		base.ignorePattern, err = regexp.Compile(opt.ignore)
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			os.Exit(-1)
		}
	}

	// config search scope
	if opt.g {
		base.scope.grep = true
	}
	if opt.grep_only {
		base.scope.dir = false
		base.scope.file = false
		base.scope.symlink = false
		base.scope.grep = true
	}
	if opt.search_binary {
		base.scope.binary = true
	}
	if opt.hidden {
		base.scope.hidden = true
	}

	return base
}

func (this gorep) kick(fpath string) *channelSet {
	chsMap := makeChannelSet()
	chsReduce := makeChannelSet()

	go func() {
		waitMaps.Add(1)
		this.mapfork(fpath, chsMap)
		waitMaps.Wait()
		closeChannelSet(chsMap)
	}()

	go func() {
		this.reduce(chsMap, chsReduce)
	}()
	return chsReduce
}

func makeChannelSet() *channelSet {
	return &channelSet{
		dir: make(chan string),
		file: make(chan string),
		symlink: make(chan string),
		line: make(chan string),
	}
}

func closeChannelSet(chans *channelSet) {
	close(chans.dir)
	close(chans.file)
	close(chans.symlink)
	close(chans.line)
}

func verifyHidden(fpath string) bool {
	byteStr := []byte(path.Base(fpath))
	// don't consider current directory(./) and parent directory(../)
	if '.' == byteStr[0] {
		return true
	}
	return false
}

func (this gorep) mapfork(fpath string, chans *channelSet) {
	defer waitMaps.Done()

	/* expand dir */
	list, err := ioutil.ReadDir(fpath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "dive error: %v\n", err)
		os.Exit(-1)
	}

	const ignoreFlag = os.ModeDir | os.ModeAppend | os.ModeExclusive | os.ModeTemporary |
						os.ModeSymlink | os.ModeDevice | os.ModeNamedPipe | os.ModeSocket |
						os.ModeSetuid | os.ModeSetgid | os.ModeCharDevice | os.ModeSticky

	for _, finfo := range list {
		mode := finfo.Mode()
		fname := finfo.Name()
		if !this.scope.hidden && verifyHidden(fname) {
			continue
		}
		if this.ignorePattern != nil && this.ignorePattern.MatchString(fname) {
			continue
		}
		switch true {
		case mode & os.ModeDir != 0:
			fullpath := fpath + separator + fname
			chans.dir <- fullpath
			waitMaps.Add(1)
			go this.mapfork(fullpath, chans)
		case mode & os.ModeSymlink != 0:
			chans.symlink <- fpath + separator + fname
		case mode & ignoreFlag == 0:
			chans.file <- fpath + separator + fname
		default:
			continue
		}
	}
}

func (this gorep) reduce(chsIn *channelSet, chsOut *channelSet) {
	filter := func(msg string, out chan<- string) {
		if this.pattern.MatchString(path.Base(msg)) {
			out <- msg
		}
	}

	// directory
	go func(in <-chan string, out chan<- string) {
		for msg := range in {
			if this.scope.dir {
				filter(msg, out)
			}
		}
		close(out)
	}(chsIn.dir, chsOut.dir)

	// file
	go func(in <-chan string, out chan<- string, chLine chan<- string) {
		for msg := range in {
			if this.scope.file {
				filter(msg, out)
			}
			if this.scope.grep {
				waitGreps.Add(1)
				go this.grep(msg, chLine)
			}
		}
		close(out)
		waitGreps.Wait()
		close(chLine)
	}(chsIn.file, chsOut.file, chsOut.line)

	// symlink
	go func(in <-chan string, out chan<- string) {
		for msg := range in {
			if this.scope.symlink {
				filter(msg, out)
			}
		}
		close(out)
	}(chsIn.symlink, chsOut.symlink)
}

// Charactor code 0x00 - 0x08 is control code (ASCII)
func verifyBinary(buf []byte) bool {
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

func (this gorep) grep(fpath string, out chan<- string) {
	defer func() {
		<- semFopenLimit
		waitGreps.Done()
	}()

	semFopenLimit <- 1
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
		fmt.Fprintf(os.Stderr, "grep mmap error: %v\n", err)
		return
	}
	defer syscall.Munmap(mem)

	isBinary := verifyBinary(mem)
	if isBinary && !this.scope.binary {
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
			if isBinary {
				formattedline := fmt.Sprintf("Binary file %s matches", fpath)
				out <- formattedline
				return
			} else {
				if this.ignorePattern != nil && this.ignorePattern.MatchString(strline) {
					continue
				}
				formattedline := fmt.Sprintf("%s:%d: %s", fpath, lineNumber, strline)
				out <- formattedline
			}
		}
	}
}

