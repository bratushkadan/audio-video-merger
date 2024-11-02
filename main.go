package main

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
)

const (
	MergedFilePrefix = "[MERGED]"
)

var (
	mediaExts = []string{".mp3", ".mp4", ".webm"}
)

func mp3Mp4Merger(ctx context.Context, errs chan<- error, videoPath, mp3Path string) {
	dstFilename := MergedFilePrefix + " " + videoPath
	var cmd *exec.Cmd
	switch ext := filepath.Ext(videoPath); ext {
	case ".mp4":
		cmd = exec.CommandContext(ctx, "ffmpeg", "-i", videoPath, "-i", mp3Path, "-c", "copy", dstFilename)
	case ".webm":
		cmd = exec.CommandContext(ctx, "ffmpeg", "-i", videoPath, "-i", mp3Path, "-c:v", "copy", "-c:a", "libvorbis", dstFilename)
	default:
		errs <- fmt.Errorf(`unrecognized file extension "%s" of file "%s"`, ext, videoPath)
		return
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr

	log.Printf(`Merging "%s" and "%s"`, videoPath, mp3Path)
	if err := cmd.Run(); err != nil {
		cmdErr := fmt.Errorf("command error: '%s'", stderr.String())
		errs <- fmt.Errorf("%w: %w", err, cmdErr)
		return
	}
	log.Printf(`Merged "%s" and "%s" to "%s"`, videoPath, mp3Path, dstFilename)

	for _, filepath := range []string{videoPath, mp3Path} {
		err := os.Remove(filepath)
		if err != nil {
			errs <- fmt.Errorf(`failed to remove file "%s": %w`, filepath, err)
			return
		}
		log.Printf(`Removed "%s"`, filepath)
	}
}

func filenameFromBasename(basename string) string {
	return strings.TrimSuffix(basename, filepath.Ext(basename))
}

func newBucket(capacity int) chan struct{} {
	b := make(chan struct{}, capacity)
	for range capacity {
		b <- struct{}{}
	}
	return b
}

func main() {
	cwd, err := os.Getwd()
	if err != nil {
		log.Fatal(err)
	}
	dirs, err := os.ReadDir(cwd)
	if err != nil {
		log.Fatal(err)
	}

	fileNames := make(map[string][]string)
	for _, dirent := range dirs {
		if dirent.IsDir() {
			continue
		}

		fileExt := filepath.Ext(dirent.Name())
		if !slices.Contains(mediaExts, fileExt) || strings.HasPrefix(dirent.Name(), MergedFilePrefix) {
			continue
		}

		fileName := filenameFromBasename(dirent.Name())
		exts, ok := fileNames[fileName]
		if !ok {
			exts = make([]string, 0)
		}
		exts = append(exts, fileExt)
		fileNames[fileName] = exts
	}

	for fileName, exts := range fileNames {
		if !slices.Contains(exts, ".mp3") || len(exts) < 2 {
			delete(fileNames, fileName)
			continue
		}
		audioExt := ".mp3"
		videoExt := exts[1-slices.Index(exts, ".mp3")]
		fileNames[fileName] = []string{videoExt, audioExt}
	}

	nProc := runtime.NumCPU() / 2
	scheduled := newBucket(nProc)
	errs := make(chan error, nProc)

	var wg sync.WaitGroup
	wg.Add(len(fileNames))
	go func() {
		for err := range errs {
			log.Print(err)
		}
	}()
	for fileName, exts := range fileNames {
		<-scheduled
		ctx := context.Background()
		go func() {
			defer func() {
				wg.Done()
				scheduled <- struct{}{}
			}()
			mp3Mp4Merger(ctx, errs, fileName+exts[0], fileName+exts[1])
		}()
	}
	wg.Wait()
	close(errs)

	// fmt.Printf("%#v\n", fileNames)
}
