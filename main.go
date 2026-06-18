package main

import (
	"fmt"
	"image/color"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/canvas"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

type FileEntry struct {
	path   string
	size   int64
	isDir  bool
	dirSz  atomic.Int64
}

type entryWithParent struct {
	entry  *FileEntry
	parent string
}

type AppState struct {
	allFiles  map[string][]*FileEntry // path -> children cache
	mu        sync.RWMutex
	win       fyne.Window
	currentDir string
	pathHistory []string
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

func sortEntries(entries []*FileEntry) {
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].isDir != entries[j].isDir {
			return entries[i].isDir
		}
		si := entries[i].size
		if entries[i].isDir {
			si = entries[i].dirSz.Load()
		}
		sj := entries[j].size
		if entries[j].isDir {
			sj = entries[j].dirSz.Load()
		}
		return si > sj
	})
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Fast File Scanner")
	myWindow.Resize(fyne.NewSize(900, 600))

	state := &AppState{allFiles: make(map[string][]*FileEntry), win: myWindow}
	statusLabel := widget.NewLabel("Ready to scan.")

	var fileList *widget.List
	
	navigate := func(dir string) {
		state.mu.Lock()
		state.currentDir = dir
		if entries, ok := state.allFiles[dir]; ok {
			sortEntries(entries)
		}
		state.mu.Unlock()
		myWindow.SetTitle(fmt.Sprintf("Fast File Scanner - %s", dir))
		fileList.Refresh()
	}

	fileList = widget.NewList(
		func() int {
			state.mu.RLock()
			defer state.mu.RUnlock()
			return len(state.allFiles[state.currentDir])
		},
		func() fyne.CanvasObject {
			return container.NewBorder(nil, nil, nil,
				container.NewHBox(widget.NewLabel(""), widget.NewButton("Del", nil)),
				canvas.NewText("", color.White))
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			state.mu.RLock()
			entries := state.allFiles[state.currentDir]
			if i >= len(entries) {
				state.mu.RUnlock()
				return
			}
			entry := entries[i]
			state.mu.RUnlock()

			c := o.(*fyne.Container)
			nameText := c.Objects[0].(*canvas.Text)
			rightBox := c.Objects[1].(*fyne.Container)
			sizeLabel := rightBox.Objects[0].(*widget.Label)
			btn := rightBox.Objects[1].(*widget.Button)

			if entry.isDir {
				nameText.Color = color.RGBA{100, 200, 255, 255}
				nameText.TextStyle = fyne.TextStyle{Bold: true}
			} else {
				nameText.Color = color.White
				nameText.TextStyle = fyne.TextStyle{}
			}
			
			sz := entry.size
			if entry.isDir {
				sz = entry.dirSz.Load()
			}
			nameText.Text = filepath.Base(entry.path)
			nameText.Refresh()
			sizeLabel.SetText(formatSize(sz))

			btn.OnTapped = func() {
				dialog.ShowConfirm("Delete", "Delete "+entry.path+"?", func(ok bool) {
					if ok {
						os.RemoveAll(entry.path)
						state.mu.Lock()
						delete(state.allFiles, entry.path)
						state.allFiles[state.currentDir] = append(entries[:i], entries[i+1:]...)
						state.mu.Unlock()
						fileList.Refresh()
					}
				}, myWindow)
			}
		},
	)

	fileList.OnSelected = func(id widget.ListItemID) {
		state.mu.RLock()
		entries := state.allFiles[state.currentDir]
		if id >= len(entries) {
			state.mu.RUnlock()
			return
		}
		entry := entries[id]
		state.mu.RUnlock()

		if entry.isDir {
			state.mu.Lock()
			state.pathHistory = append(state.pathHistory, state.currentDir)
			state.mu.Unlock()
			navigate(entry.path)
		}
		fileList.UnselectAll()
	}

	myWindow.Canvas().SetOnTypedKey(func(k *fyne.KeyEvent) {
		if k.Name == fyne.KeyBackspace {
			state.mu.Lock()
			if len(state.pathHistory) > 0 {
				prev := state.pathHistory[len(state.pathHistory)-1]
				state.pathHistory = state.pathHistory[:len(state.pathHistory)-1]
				state.mu.Unlock()
				navigate(prev)
			} else {
				state.mu.Unlock()
			}
		}
	})

	scanButton := widget.NewButton("Select Directory to Scan", func() {
		dialog.ShowFolderOpen(func(list fyne.ListableURI, err error) {
			if err != nil || list == nil {
				return
			}
			state.mu.Lock()
			state.allFiles = make(map[string][]*FileEntry)
			state.currentDir = list.Path()
			state.pathHistory = []string{}
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
	entryChan := make(chan entryWithParent, 10000)
	var wg sync.WaitGroup

	go func() {
		batch := make([]entryWithParent, 0, 1000)
		ticker := time.NewTicker(200 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case e, ok := <-entryChan:
				if !ok {
					if len(batch) > 0 {
						state.mu.Lock()
						for _, ep := range batch {
							state.allFiles[ep.parent] = append(state.allFiles[ep.parent], ep.entry)
						}
						state.mu.Unlock()
					}
					state.mu.Lock()
					for _, entries := range state.allFiles {
						sortEntries(entries)
					}
					count := len(state.allFiles)
					state.mu.Unlock()
					fyne.Do(func() {
						list.Refresh()
						status.SetText(fmt.Sprintf("Scan complete. %d directories.", count))
					})
					return
				}
				batch = append(batch, e)
				if len(batch) >= 1000 {
					state.mu.Lock()
					for _, ep := range batch {
						state.allFiles[ep.parent] = append(state.allFiles[ep.parent], ep.entry)
					}
					state.mu.Unlock()
					batch = batch[:0]
				}
			case <-ticker.C:
				if len(batch) > 0 {
					state.mu.Lock()
					for _, ep := range batch {
						state.allFiles[ep.parent] = append(state.allFiles[ep.parent], ep.entry)
					}
					state.mu.Unlock()
					batch = batch[:0]
				}
				state.mu.RLock()
				count := len(state.allFiles)
				state.mu.RUnlock()
				fyne.Do(func() {
					list.Refresh()
					status.SetText(fmt.Sprintf("Scanning... %d directories.", count))
				})
			}
		}
	}()

	wg.Add(1)
	go scanDir(dir, entryChan, &wg)

	go func() {
		wg.Wait()
		close(entryChan)
	}()
}

func scanDir(dir string, out chan<- entryWithParent, wg *sync.WaitGroup) {
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
		out <- entryWithParent{entry: e, parent: dir}

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
