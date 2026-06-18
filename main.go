package main

import (
	"fmt"
	"io/fs"
	"path/filepath"
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"
)

// AppState holds our data and a mutex to prevent race conditions
// between the UI thread reading the list and the scanner writing to it.
type AppState struct {
	files []string
	mu    sync.RWMutex
}

func main() {
	myApp := app.New()
	myWindow := myApp.NewWindow("Fast File Scanner")
	myWindow.Resize(fyne.NewSize(800, 600))

	state := &AppState{
		files: make([]string, 0),
	}

	// 1. Setup UI Components
	statusLabel := widget.NewLabel("Ready to scan.")
	
	// Fyne's List widget is virtualized. We pass it callbacks to handle the data.
	fileList := widget.NewList(
		func() int {
			state.mu.RLock()
			defer state.mu.RUnlock()
			return len(state.files)
		},
		func() fyne.CanvasObject {
			// Template for the rows
			return widget.NewLabel("Waiting for data...")
		},
		func(i widget.ListItemID, o fyne.CanvasObject) {
			// Update the row with actual data
			state.mu.RLock()
			defer state.mu.RUnlock()
			if i < len(state.files) {
				o.(*widget.Label).SetText(state.files[i])
			}
		},
	)

	// 2. The Scan Button Logic
	scanButton := widget.NewButton("Select Directory to Scan", func() {
		// Open a folder selection dialog
		dialog.ShowFolderOpen(func(list fyne.ListableURI, err error) {
			if err != nil || list == nil {
				return // User cancelled or error
			}
			
			// Reset state for new scan
			state.mu.Lock()
			state.files = []string{}
			state.mu.Unlock()
			fileList.Refresh()
			statusLabel.SetText("Scanning...")

			// Start the concurrent scan
			startScan(list.Path(), state, fileList, statusLabel)
			
		}, myWindow)
	})

	// 3. Layout the Window
	// Button and status at the top, the virtualized list taking up the rest of the space
	topContent := container.NewVBox(scanButton, statusLabel)
	mainContent := container.NewBorder(topContent, nil, nil, nil, fileList)

	myWindow.SetContent(mainContent)
	myWindow.ShowAndRun()
}

// startScan orchestrates the background scanning and batched UI updates
func startScan(dir string, state *AppState, list *widget.List, status *widget.Label) {
	pathChan := make(chan string, 5000)

	// A. The UI Updater Goroutine
	go func() {
		var batch []string
		// Ticker to ensure the UI updates periodically even if a batch isn't full
		ticker := time.NewTicker(100 * time.Millisecond)
		defer ticker.Stop()

		for {
			select {
			case path, ok := <-pathChan:
				if !ok {
					// Channel closed, scan is complete. Flush remaining.
					if len(batch) > 0 {
						state.mu.Lock()
						state.files = append(state.files, batch...)
						state.mu.Unlock()
					}
					list.Refresh()
					
					// Update status safely
					state.mu.RLock()
					count := len(state.files)
					state.mu.RUnlock()
					status.SetText(fmt.Sprintf("Scan complete. Found %d files.", count))
					return
				}

				batch = append(batch, path)
				
				// If batch reaches 500, flush to state
				if len(batch) >= 500 {
					state.mu.Lock()
					state.files = append(state.files, batch...)
					state.mu.Unlock()
					batch = batch[:0] // Clear the batch efficiently
				}

			case <-ticker.C:
				// Periodically flush and refresh the UI so the user sees progress
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
				status.SetText(fmt.Sprintf("Scanning... Found %d files.", count))
			}
		}
	}()

	// B. The File System Walker Goroutine
	go func() {
		defer close(pathChan)
		filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err == nil {
				pathChan <- p
			}
			return nil
		})
	}()
}
