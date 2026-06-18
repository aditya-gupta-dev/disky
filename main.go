package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

type FileEntry struct {
	path   string
	size   int64
	isDir  bool
	dirSz  atomic.Int64 // for dirs, calculated in background
}

type AppState struct {
	files   []*FileEntry
	mu      sync.RWMutex
	win     fyne.Window
	rootDir string
}

func formatSize(sz int64) string {
	const unit = 1024
	if sz < unit {
		return fmt.Sprintf("%d B", sz)
	}
	div, exp := int64(unit), 0
	for n := sz / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(sz)/float64(div), "KMGTPE"[exp])
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Fast File Scanner")
	myWindow.Resize(fyne.NewSize(900, 600))

	state := &AppState{files: []*FileEntry{}, win: myWindow}
	statusLabel := widget.NewLabel("Ready to scan.")

	var fileList *widget.List
	fileList = widget.NewList(
		func() int {
			state.mu.RLock()
			defer state.mu.RUnlock()
			return len(state.files)
		},
		func() fyne.CanvasObject {
			return container.NewBorder(nil, nil, nil,
				container.NewHBox(widget.NewLabel(""), widget.NewButton("Del", nil)),
				widget.NewLabel(""))
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			state.mu.RLock()
			if i >= len(state.files) {
				state.mu.RUnlock()
				return
			}
			entry := state.files[i]
			state.mu.RUnlock()

			c := o.(*fyne.Container)
			nameLabel := c.Objects[0].(*widget.Label)
			rightBox := c.Objects[1].(*fyne.Container)
			sizeLabel := rightBox.Objects[0].(*widget.Label)
			btn := rightBox.Objects[1].(*widget.Button)

			sz := entry.size
			if entry.isDir {
				sz = entry.dirSz.Load()
			}
			nameLabel.SetText(filepath.Base(entry.path))
			sizeLabel.SetText(formatSize(sz))

			btn.OnTapped = func() {
				dialog.ShowConfirm("Delete", "Delete "+entry.path+"?", func(ok bool) {
					if ok {
						os.RemoveAll(entry.path)
						state.mu.Lock()
						state.files = append(state.files[:i], state.files[i+1:]...)
						state.mu.Unlock()
						fileList.Refresh()
					}
				}, myWindow)
			}
		},
	)

	scanButton := widget.NewButton("Select Directory to Scan", func() {
		dialog.ShowFolderOpen(func(list fyne.ListableURI, err error) {
			if err != nil || list == nil {
				return
			}
			state.mu.Lock()
			state.files = []*FileEntry{}
			state.rootDir = list.Path()
			state.mu.Unlock()
			myWindow.SetTitle(fmt.Sprintf("Fast File Scanner - %s", list.Path()))
			fileList.Refresh()
			statusLabel.SetText("Scanning...")
			startScan(list.Path(), state, fileList, statusLabel)
		}, myWindow)
	})

	topContent := container.NewVBox(scanButton, statusLabel)
	mainContent := container.NewBorder(topContent, nil, nil, nil, fileList)
	myWindow.SetContent(mainContent)
	myWindow.ShowAndRun()
}

func startScan(dir string, state *AppState, list *widget.List, status *widget.Label) {
	entryChan := make(chan *FileEntry, 10000)
	var wg sync.WaitGroup

	// UI updater
	go func() {
		batch := make([]*FileEntry, 0, 1000)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case e, ok := <-entryChan:
				if !ok {
					if len(batch) > 0 {
						state.mu.Lock()
						state.files = append(state.files, batch...)
						state.mu.Unlock()
					}
					list.Refresh()
					state.mu.RLock()
					count := len(state.files)
					state.mu.RUnlock()
					status.SetText(fmt.Sprintf("Scan complete. %d items.", count))
					return
				}
				batch = append(batch, e)
				if len(batch) >= 1000 {
					state.mu.Lock()
					state.files = append(state.files, batch...)
					state.mu.Unlock()
					batch = batch[:0]
				}
			case <-ticker.C:
				if len(batch) > 0 {
					state.mu.Lock()
					state.files = append(state.files, batch...)
					state.mu.Unlock()
					batch = batch[:0]
				}
				list.Refresh()
				state.mu.RLock()
				count := len(state.files)
				state.mu.RUnlock()
				status.SetText(fmt.Sprintf("Scanning... %d items.", count))
			}
		}
	}()

	// Walker spawns goroutine per directory
	wg.Add(1)
	go scanDir(dir, entryChan, &wg)

	go func() {
		wg.Wait()
		close(entryChan)
	}()
}

func scanDir(dir string, out chan<- *FileEntry, wg *sync.WaitGroup) {
	defer wg.Done()

	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}

	for _, d := range entries {
		p := filepath.Join(dir, d.Name())
		info, err := d.Info()
		if err != nil {
			continue
		}

		e := &FileEntry{path: p, size: info.Size(), isDir: d.IsDir()}
		out <- e

		if d.IsDir() {
			wg.Add(1)
			go scanDir(p, out, wg)
			go calcDirSize(p, e)
		}
	}
}

func calcDirSize(dir string, entry *FileEntry) {
	var total int64
	filepath.WalkDir(dir, func(_ string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			if info, err := d.Info(); err == nil {
				total += info.Size()
			}
		}
		return nil
	})
	entry.dirSz.Store(total)
}
