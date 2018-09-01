package main

import (
	"bufio"
	"context"
	"fmt"
	"log"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/chromedp/chromedp"
	"github.com/chromedp/chromedp/runner"
	flag "github.com/spf13/pflag"
)

// Worker represents the tasks for a thread
type Worker struct {
	ctx        *context.Context
	chrome     *chromedp.Res
	dir        string
	dimensions [2]int
	pool       *chromedp.Pool
	tasks      []Task
	wait       time.Duration
	wg         *sync.WaitGroup
}

// Load naviagates to the given URL, and waits for the page to load
func (w *Worker) Load(u string) error {
	c, err := w.pool.Allocate(*w.ctx, runner.WindowSize(w.dimensions[0], w.dimensions[1]))
	if err != nil {
		return fmt.Errorf("error allocating new instance from pool: %v", err)
	}
	w.chrome = c
	tasks := chromedp.Tasks{
		chromedp.Navigate(u),
	}
	err = w.chrome.Run(*w.ctx, tasks)
	if err == nil {
		time.Sleep(w.wait)
	}
	return err
}

// Work reads URLs from the given channel, loads them, and then performs any
// tasks on the loaded page.
func (w *Worker) Work(urlsChan <-chan string, errorChan chan error) {
	for {
		u, more := <-urlsChan
		if !more {
			w.wg.Done()
			return
		}

		err := w.Load(u)
		if err != nil {
			errorChan <- fmt.Errorf("failed to load %v: %v", u, err)
			continue
		}

		d := path.Join(w.dir, strings.Replace(u, "://", "-", 1))
		os.MkdirAll(d, os.ModePerm)
		for i := uint8(1); i <= 3; i++ {
			for _, t := range w.tasks {
				if t.Priority() == i {
					if err = t.Run(*w.ctx, u, d, w.chrome); err != nil {
						errorChan <- fmt.Errorf("failed to run task: %v", err)
					}
				}
			}
		}
		w.chrome.Release()
	}
}

func main() {
	// Argument parsing
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage of %s:\n", os.Args[0])
		fmt.Fprintf(os.Stderr, "%s [OPTIONS]... [TARGETS FILE]\n", os.Args[0])
		flag.PrintDefaults()
	}

	numThreads := flag.IntP("threads", "t", 10, "Number of threads to run")
	wait := flag.DurationP("wait", "w", 2*time.Second, "Number of milliseconds to wait for page to load before running tasks")
	relDir := flag.StringP("output", "o", "spydom_output", "The directory to store output in")
	width := flag.IntP("width", "", 1920, "The width of the chrome window to use")
	height := flag.IntP("height", "", 1080, "The height of the chrome window to use")
	flag.Parse()

	if flag.NArg() != 1 || flag.Arg(0) == "" {
		fmt.Println("Please supply a targets file")
		flag.Usage()
		os.Exit(1)
	}
	dir, err := filepath.Abs(*relDir)
	if err != nil {
		log.Fatalf("Failed to open output directory: %v\n", err)
	}

	// Create a new chrome pool and the workers
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool, err := chromedp.NewPool()
	if err != nil {
		log.Fatalf("failed to create chromedp pool: %v\n", err)
	}

	urlsChan := make(chan string)
	errorChan := make(chan error)
	wg := &sync.WaitGroup{}
	wg.Add(*numThreads)
	workers := make([]*Worker, *numThreads)
	tasks := getTasks()
	for i := range workers {
		w := &Worker{
			ctx:        &ctx,
			dir:        dir,
			dimensions: [2]int{*width, *height},
			pool:       pool,
			wg:         wg,
			tasks:      tasks,
			wait:       *wait,
		}
		workers[i] = w
		go w.Work(urlsChan, errorChan)
	}

	// Read targets line by line and dispatch to workers
	tfile, err := os.Open(flag.Arg(0))
	if err != nil {
		log.Fatalf("Failed to open targets file: %v\n", err)
	}
	defer tfile.Close()

	tscanner := bufio.NewScanner(tfile)
	re := regexp.MustCompile("^https?://")
	go func() {
		defer close(urlsChan)
		for tscanner.Scan() {
			u := tscanner.Text()
			if !re.MatchString(u) {
				u = "https://" + u
			}

			log.Println(u)
			urlsChan <- u
		}

		if err = tscanner.Err(); err != nil {
			log.Fatalf("Error while reading targets file: %v\n", err)
		}
	}()

	// Report errors
	go func() {
		l := log.New(os.Stderr, "ERROR: ", 0)
		for {
			err := <-errorChan
			l.Println(err)
		}
	}()

	wg.Wait()
}
