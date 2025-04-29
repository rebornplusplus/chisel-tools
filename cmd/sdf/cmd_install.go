package main

import (
	"fmt"
	"math/rand/v2"
	"os"
	"os/exec"

	"github.com/rebornplusplus/chisel-tools/internal/chisel"
)

type cmdInstall struct {
	Arch    []string `short:"a" long:"arch" description:"Package architecture"`
	Release string   `short:"r" long:"release" description:"Chisel release path" required:"true"`
	Prune   bool     `short:"p" long:"prune" description:"Install a slice once"`
	Worker  int      `short:"w" long:"worker" description:"Number of concurrent workers" default:"10"`
	Args    struct {
		Files []string `positional-arg-name:"slice definition files"`
	} `positional-args:"yes"`
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
	if len(c.Args.Files) == 0 {
		return nil
	}
	if len(c.Arch) == 0 {
		c.Arch = []string{"amd64"}
	}

	var sliceDefs []*chisel.Slice
	for _, f := range c.Args.Files {
		defs, err := chisel.ParseSlices(f)
		if err != nil {
			return fmt.Errorf("cannot parse slice definition file %s: %w",
				f, err)
		}
		sliceDefs = append(sliceDefs, defs...)
	}
	if c.Prune {
		sliceDefs = prune(sliceDefs)
	}
	return c.installSlices(sliceDefs)
}

func prune(sliceDefs []*chisel.Slice) []*chisel.Slice {
	// Shuffle the slice.
	defs := make([]*chisel.Slice, len(sliceDefs))
	perm := rand.Perm(len(sliceDefs))
	for i, v := range perm {
		defs[v] = sliceDefs[i]
	}
	// If a slice is going to be installed as an essential, do not install it a
	// second time on it's own.
	pending := make(map[string]bool)
	for _, s := range defs {
		pending[s.Name] = true
	}
	var todo []*chisel.Slice
	for _, s := range defs {
		if _, ok := pending[s.Name]; !ok {
			continue
		}
		todo = append(todo, s)
		for _, e := range s.Essential {
			delete(pending, e)
		}
	}
	return todo
}

func (c *cmdInstall) installSlices(sliceDefs []*chisel.Slice) error {
	nTasks := len(c.Arch) * len(sliceDefs)
	tasks := make(chan *task, nTasks)
	errs := make(chan error, nTasks)
	done := make(chan bool, c.Worker)

	for i := 0; i < c.Worker; i++ {
		go do(tasks, errs, done)
	}

	for _, a := range c.Arch {
		for _, s := range sliceDefs {
			tasks <- &task{
				slice:   s.Name,
				arch:    a,
				release: c.Release,
			}
		}
	}
	close(tasks)

	var finished int
	for finished < c.Worker {
		select {
		case err := <-errs:
			if err != nil {
				fmt.Fprintln(os.Stderr, err)
			}
		case <-done:
			finished++
		}
	}
	return nil
}

type task struct {
	slice   string
	arch    string
	release string
}

func do(tasks <-chan *task, errs chan<- error, done chan<- bool) {
	dir, err := os.MkdirTemp("", "")
	if err != nil {
		errs <- fmt.Errorf("cannot create temp directory: %w", err)
	}
	defer os.RemoveAll(dir)

	for task := range tasks {
		root, err := os.MkdirTemp(dir, "chisel-")
		if err != nil {
			errs <- fmt.Errorf("cannot create temp directory: %w", err)
			continue
		}

		if opts.Verbose {
			fmt.Fprintf(os.Stderr, "installing %s slice for %s arch...\n",
				task.slice, task.arch)
		}

		cmd := exec.Command(
			"chisel",
			"cut",
			"--release", task.release,
			"--arch", task.arch,
			"--root", root,
			task.slice,
		)
		output, err := cmd.CombinedOutput()
		if err == nil {
			if opts.Verbose {
				fmt.Fprintf(os.Stderr, "[SUCCESS] installed %s slice on %s\n",
					task.slice, task.arch)
			}
			continue
		}

		if _, ok := err.(*exec.ExitError); !ok {
			errs <- fmt.Errorf("cannot execute process: %w", err)
			continue
		}
		fmt.Fprintf(os.Stderr, "[FAILED] could not install %s on %s:\n"+
			"===========================================\n"+
			"%s\n"+
			"===========================================\n",
			task.slice, task.arch, output,
		)
	}
	done <- true
}
