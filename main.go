package main

import (
	"context"
	"errors"
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

func mergeAudVid() {
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
}

type tmpFile struct {
	dir string

	file *os.File
}

func (f *tmpFile) File() *os.File {
	return f.file
}

func (f *tmpFile) Create() (*os.File, error) {
	dir, err := os.MkdirTemp("", "concat-files")
	if err != nil {
		return nil, fmt.Errorf("error creating temporary directory for list of videos: %v", err)
	}
	f.dir = dir

	file, err := os.CreateTemp(dir, "video-file-list")
	if err != nil {
		errRmDir := os.Remove(f.dir)
		if errRmDir != nil {
			return nil, fmt.Errorf("error creating temp file for the list of videos: %v, error cleaning up temp dir: %v", err, errRmDir)
		}
		return nil, fmt.Errorf("error creating temp file for the list of videos: %v", err)
	}

	f.file = file

	return f.file, nil
}

func (f *tmpFile) Cleanup() error {
	var errs []error
	var err = f.file.Close()
	if err != nil {
		errs = append(errs, err)
	}
	err = os.RemoveAll(f.dir)
	if err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return errors.Join(errs...)
	}
	return nil
}

func concatInputFiles(files []string) string {
	mappedFiles := make([]string, 0, len(files))
	for _, v := range files {
		mappedFiles = append(mappedFiles, fmt.Sprintf("file 'file:%s'", v))
	}

	return strings.Join(mappedFiles, "\n")
}

func concatVideos(ctx context.Context) error {
	files := os.Args[1:]
	switch len(files) {
	case 0:
		log.Fatal("no video files to concat provided")
	case 1:
		log.Fatal("provide more than one video to concat")
	}

	file, err := os.Create("list.txt")
	if err != nil {
		return err
	}
	defer func() {
		err := file.Close()
		if err != nil {
			log.Println(fmt.Errorf("failed to close file: %v", err))
			return
		}
		err = os.Remove(file.Name())
		if err != nil {
			log.Println(fmt.Errorf("failed to clean up: %v", err))
		}
	}()

	_, err = file.WriteString(concatInputFiles(files))
	if err != nil {
		return err
	}

	args := []string{"-f", "concat", "-safe", "0", "-i", file.Name(), fmt.Sprintf("concat - %s", files[0])}
	cmd := exec.CommandContext(ctx, "ffmpeg", args...)

	cmd.Stderr = os.Stderr
	cmd.Stdout = os.Stdout

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("failed to run ffmpeg concat command: %v", err)
	}

	return nil
}

func main() {
	if err := concatVideos(context.TODO()); err != nil {
		log.Fatal(err)
	}
	// mergeAudVid()
}
