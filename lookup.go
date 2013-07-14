package main

import (
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
	path     string
	line     string
}

type lookup struct {
	bRecursive bool
	bFind      bool
	bGrep      bool
	pattern         *regexp.Regexp
}

func usage(progName string) {
	fmt.Printf("%s [-r] [-f] [-g] PATTERN PATH\n", path.Base(progName))
}

func main() {
	cpus := runtime.NumCPU()
	runtime.GOMAXPROCS(cpus)

	/* parse flag */
	requireRecursive := flag.Bool("r", true, "enable recursive search.")
	requireFile      := flag.Bool("f", true, "enable file search.")
	requireGrep      := flag.Bool("g", false, "enable grep.")
	flag.Parse()

	if (flag.NArg() < 2) {
		usage(os.Args[0])
		os.Exit(0)
	}

	pattern := flag.Arg(0)
	path    := flag.Arg(1)

	fmt.Printf("pattern:%s path:%s\n", pattern, path)

	/* create lookup */
	c := New(*requireRecursive, *requireFile, *requireGrep, pattern)

	/* make notify channel */
	chNotify := make(chan report)

	/* start lookup */
	go c.kick(path, chNotify)

	showReport(chNotify)
}

func showReport(chNotify <-chan report) {
	/* receive notify */
	for repo, ok := <-chNotify; ok; repo, ok = <-chNotify {
		switch repo.fmode {
		case FMODE_DIR:
			fmt.Println("[Dir ]: ", repo.path)
		case FMODE_FILE:
			fmt.Println("[File]: ", repo.path)
		case FMODE_LINE:
			fmt.Println("[Grep]: ", repo.path, ": ", repo.line)
		default:
			fmt.Fprintf(os.Stderr, "Illegal filemode (%d)\n", repo.fmode)
		}
	}
}

func New(requireRecursive, requireFile, requireGrep bool, pattern string) *lookup {
	compiledPattern, err := regexp.Compile(pattern)
    if err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
        os.Exit(1)
    }
	return &lookup{requireRecursive, requireFile, requireGrep, compiledPattern}
}

func (this lookup) kick(path string, chNotify chan<- report) {
	/* make child channel */
	chRelay := make(chan report, 10)
	nRoutines := 0

	nRoutines++
	go this.dive(path, chRelay)

	for nRoutines > 0 {
		relayRepo := <-chRelay

		if relayRepo.complete {
			nRoutines--
			continue
		}

		switch relayRepo.fmode {
		case FMODE_DIR:
			if this.pattern.MatchString(relayRepo.path) {
				chNotify <-relayRepo
			}
			if (this.bRecursive) {
				nRoutines++
				go this.dive(relayRepo.path, chRelay)
			}
		case FMODE_FILE:
			if this.pattern.MatchString(relayRepo.path) {
				chNotify <-relayRepo
			}
			if (this.bGrep) {
				nRoutines++
				go this.grep(relayRepo.path, chRelay)
			}
		case FMODE_LINE:
			chNotify <-relayRepo
		default:
			fmt.Fprintf(os.Stderr, "Illegal filemode (%d)\n", relayRepo.fmode)
		}
	}

	close(chRelay)
	close(chNotify)
}

func (this lookup) dive(dir string, chRelay chan<- report) {
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
		chRelay <-report{false, fmode, dir+"/"+finfo.Name(), ""}
	}

	chRelay <-report{true, FMODE_INVALID, "", ""}
}

func (this lookup) grep(dir string, chRelay chan<- report) {
	/* T.B.D */
}

