package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"strings"
	"sync"

	"github.com/rebornplusplus/chisel-tools/internal/chisel"
)

type cmdInstall struct {
	Release string `short:"r" long:"release" description:"Chisel release path" required:"true"`
	Arch    string `short:"a" long:"arch" description:"Package architecture" default:"amd64"`

	Combine bool `long:"combine" description:"Install all slices in one go"`
	Prune   bool `long:"prune" description:"Install only the top level slices"`

	Workers  int  `short:"w" long:"workers" description:"Number of concurrent workers" default:"10"`
	Continue bool `short:"c" long:"continue-on-error" description:"Continue on installation errors"`

	Ignore bool `long:"ignore-missing" description:"Ignore missing packages for an arch"`
	Ensure bool `long:"ensure-existence" description:"Ensure package existence for at least one arch"`

	Args struct {
		Files []string `positional-arg-name:"slice definition files"`
	} `positional-args:"yes" required:"true"`
}

func init() {
	parser.AddCommand(
		"install",
		"Install slices",
		"The install command installs all slices from the specified files",
		&cmdInstall{},
	)
}

func (c *cmdInstall) Execute(args []string) error {
	if len(args) > 0 {
		return ErrExtraArgs
	}
	if c.Workers <= 0 {
		return fmt.Errorf("invalid value for --workers: %d", c.Workers)
	}
	for _, f := range c.Args.Files {
		if !strings.HasPrefix(f, c.Release) {
			return fmt.Errorf("file %s is not inside release %s", f, c.Release)
		}
	}
	if len(c.Args.Files) == 0 {
		return nil // There is nothing to do.
	}

	var slices []*chisel.Slice
	for _, f := range c.Args.Files {
		s, err := chisel.ParseSlices(f)
		if err != nil {
			return fmt.Errorf("cannot parse slices from file %s: %w", f, err)
		}
		slices = append(slices, s...)
	}

	if c.Prune {
		slices = prune(slices)
	}

	g := group(slices, c.Combine)
	return c.install(g)
}

// Group slices for installation. If combine is true, create only one group with
// all slices in it.
func group(slices []*chisel.Slice, combine bool) [][]string {
	var grouped [][]string
	if combine {
		var names []string
		for _, s := range slices {
			names = append(names, s.Name)
		}
		grouped = append(grouped, names)
	} else {
		for _, s := range slices {
			grouped = append(grouped, []string{s.Name})
		}
	}
	return grouped
}

// Prune the list of slices and return only the top-level slices that no slice
// depends on. Installing these slices alone should cover all of the slices.
// It depends on the acyclic dependency policy of chisel slices.
func prune(slices []*chisel.Slice) []*chisel.Slice {
	log.Print("Pruning the list of slices...")

	pending := make(map[string]*chisel.Slice)
	for _, s := range slices {
		pending[s.Name] = s
	}
	for _, s := range slices {
		for _, e := range s.Essential {
			delete(pending, e)
		}
	}
	var todo []*chisel.Slice
	for _, s := range pending {
		todo = append(todo, s)
	}
	return todo
}

// Install the groups of slices, concurrently.
func (c *cmdInstall) install(slices [][]string) error {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	tasks := make(chan *task, len(slices)) // Tasks to finish.
	errs := make(chan error, len(slices))  // Errors from the tasks, if any.
	for _, s := range slices {
		tasks <- &task{
			args:   []string{"cut", "--release", c.Release, "--arch", c.Arch},
			slices: s,
		}
	}
	close(tasks)

	done := make(chan bool) // Indicates that the workers are done.
	var wg sync.WaitGroup
	for range min(c.Workers, len(slices)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			worker(ctx, tasks, errs)
		}()
	}
	go func() {
		wg.Wait()
		done <- true
	}()

loop:
	for {
		select {
		case <-done:
			break loop
		case <-errs:
			if !c.Continue {
				cancel()
			}
		}
	}

	return nil
}

type task struct {
	args   []string // Chisel arguments without positional slice name(s).
	slices []string // Positional argument - slice name(s) to install.
}

func worker(ctx context.Context, tasks <-chan *task, errs chan<- error) {
	do := func(task *task) {
		name := strings.Join(task.slices, " ")
		log.Printf("Installing %s...", name)

		dir, err := os.MkdirTemp("", "")
		if err != nil {
			// Should not happen, but let's be nice and log if it happens.
			log.Printf("[NO] Failed to install %s: %s", name, err)
			errs <- err
			return
		}
		defer os.RemoveAll(dir)

		args := append(task.args, "--root", dir)
		args = append(args, task.slices...)

		cmd := exec.CommandContext(ctx, "chisel", args...)
		if out, err := cmd.CombinedOutput(); err != nil {
			if e, ok := err.(*exec.ExitError); ok && e.ProcessState.ExitCode() != -1 {
				log.Printf("[NO] Failed to install %s: %s\n%s", name, err, out)
			}
			errs <- err
		} else {
			log.Printf("[OK] Installed %s", name)
		}
	}

loop:
	for {
		select {
		case <-ctx.Done():
			break loop // Context cancelled. Quit.
		case task, ok := <-tasks:
			if !ok {
				break loop // No more tasks. Quit.
			}
			do(task)
		}
	}
}
