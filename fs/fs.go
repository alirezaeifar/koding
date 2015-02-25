// Package fs provides file system handleFuncs that can be used with kite
// library
package fs

import (
	"errors"
	"log"
	"os"
	"path"
	"sync"

	"github.com/koding/klient/Godeps/_workspace/src/github.com/koding/kite"
	"github.com/koding/klient/Godeps/_workspace/src/github.com/koding/kite/dnode"
	"github.com/koding/klient/Godeps/_workspace/src/gopkg.in/fsnotify.v1"
)

var (
	once               sync.Once // watcher variables
	newPaths, oldPaths = make(chan string), make(chan string)

	// Limit of watching folders
	// user -> path callbacks
	watchCallbacks = make(map[string]map[string]func(fsnotify.Event), 100)
	mu             sync.Mutex // protects watchCallbacks
)

func ReadDirectory(r *kite.Request) (interface{}, error) {
	var params struct {
		Path     string
		OnChange dnode.Function
	}

	if r.Args == nil {
		return nil, errors.New("arguments are not passed")
	}

	if r.Args.One().Unmarshal(&params) != nil || params.Path == "" {
		log.Println("params", params)
		return nil, errors.New("{ path: [string], onChange: [function]}")
	}

	response := make(map[string]interface{})

	if params.OnChange.IsValid() {
		onceBody := func() { startWatcher() }
		go once.Do(onceBody)

		var eventType string
		var fileEntry *FileEntry

		changer := func(ev fsnotify.Event) {
			switch ev.Op {
			case fsnotify.Create:
				eventType = "added"
				fileEntry, _ = getInfo(ev.Name)
			case fsnotify.Remove, fsnotify.Rename:
				eventType = "removed"
				fileEntry = NewFileEntry(path.Base(ev.Name), ev.Name)
			}

			event := map[string]interface{}{
				"event": eventType,
				"file":  fileEntry,
			}

			// send back the result to the client
			params.OnChange.Call(event)
			return
		}

		// first check if are watching the path, if not send it to the watcher
		mu.Lock()
		userCallbacks, ok := watchCallbacks[params.Path]
		if !ok {
			// notify new paths to the watcher
			newPaths <- params.Path
		}

		// now add the callback to the specific user. If it's already exists we just override
		_, ok = userCallbacks[r.Username]
		if !ok {
			userCallbacks[r.Username] = changer
			watchCallbacks[params.Path] = userCallbacks
		}
		mu.Unlock()

		removePath := func() {
			mu.Lock()

			userCallbacks, ok := watchCallbacks[params.Path]
			if ok {
				// delete the user callback function for this path
				delete(userCallbacks, r.Username)

				// now check if there is any user left back. If we have removed
				// all users, we should also stop the watcher from watching the
				// path. So notify the watcher to stop watching the path and
				// also remove it from the callbacks map
				if len(userCallbacks) == 0 {
					// notify the watcher that we are done with this path, because
					// all users are removed
					delete(watchCallbacks, params.Path)
					oldPaths <- params.Path
				}
			}
			mu.Unlock()
		}

		// remove the path when the remote client disconnects
		r.Client.OnDisconnect(removePath)

		// this callback is called whenever we receive a 'stopWatching' from the client
		response["stopWatching"] = dnode.Callback(func(r *dnode.Partial) {
			removePath()
		})
	}

	files, err := readDirectory(params.Path)
	if err != nil {
		return nil, err
	}

	response["files"] = files
	return response, nil
}

func startWatcher() {
	var err error
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		log.Fatal(err)
	}

	go func() {
		for {
			select {
			case p := <-newPaths:
				err := watcher.Add(p)
				if err != nil {
					log.Println("watch path adding", err)
				}
			case p := <-oldPaths:
				err := watcher.Remove(p)
				if err != nil {
					log.Println("watch remove adding", err)
				}
			}
		}
	}()

	for {
		select {
		case event := <-watcher.Events:

			mu.Lock()
			f, ok := watchCallbacks[path.Dir(event.Name)]
			mu.Unlock()

			if !ok {
				continue
			}

			f(event)

		case err := <-watcher.Errors:
			log.Println("watcher error:", err)
		}
	}

}

func Glob(r *kite.Request) (interface{}, error) {
	var params struct {
		Pattern string
	}

	if r.Args.One().Unmarshal(&params) != nil || params.Pattern == "" {
		return nil, errors.New("{ pattern: [string] }")
	}

	return glob(params.Pattern)
}

func ReadFile(r *kite.Request) (interface{}, error) {
	var params struct {
		Path string
	}

	if r.Args.One().Unmarshal(&params) != nil || params.Path == "" {
		return nil, errors.New("{ path: [string] }")
	}

	return readFile(params.Path)
}

type writeFileParams struct {
	Path           string
	Content        []byte
	DoNotOverwrite bool
	Append         bool
}

func WriteFile(r *kite.Request) (interface{}, error) {
	var params writeFileParams
	if r.Args.One().Unmarshal(&params) != nil || params.Path == "" {
		return nil, errors.New("{ path: [string] }")
	}

	return writeFile(params.Path, params.Content, params.DoNotOverwrite, params.Append)
}

func UniquePath(r *kite.Request) (interface{}, error) {
	var params struct {
		Path string
	}

	if r.Args.One().Unmarshal(&params) != nil || params.Path == "" {
		return nil, errors.New("{ path: [string] }")
	}

	return uniquePath(params.Path)
}

func GetInfo(r *kite.Request) (interface{}, error) {
	var params struct {
		Path string
	}

	if r.Args.One().Unmarshal(&params) != nil || params.Path == "" {
		return nil, errors.New("{ path: [string] }")
	}

	return getInfo(params.Path)
}

type setPermissionsParams struct {
	Path      string
	Mode      os.FileMode
	Recursive bool
}

func SetPermissions(r *kite.Request) (interface{}, error) {
	var params setPermissionsParams

	if r.Args.One().Unmarshal(&params) != nil || params.Path == "" {
		return nil, errors.New("{ path: [string], mode: [integer], recursive: [bool] }")
	}

	err := setPermissions(params.Path, params.Mode, params.Recursive)
	if err != nil {
		return nil, err
	}

	return true, nil
}

func Remove(r *kite.Request) (interface{}, error) {
	var params struct {
		Path      string
		Recursive bool
	}

	if r.Args.One().Unmarshal(&params) != nil || params.Path == "" {
		return nil, errors.New("{ path: [string], recursive: [bool] }")
	}

	if err := remove(params.Path, params.Recursive); err != nil {
		return nil, err
	}

	return true, nil
}

func Rename(r *kite.Request) (interface{}, error) {
	var params struct {
		OldPath string
		NewPath string
	}

	if r.Args.One().Unmarshal(&params) != nil || params.OldPath == "" || params.NewPath == "" {
		return nil, errors.New("{ oldPath: [string], newPath: [string] }")
	}

	err := rename(params.OldPath, params.NewPath)
	if err != nil {
		return nil, err
	}

	return true, nil
}

func CreateDirectory(r *kite.Request) (interface{}, error) {
	var params struct {
		Path      string
		Recursive bool
	}

	if r.Args.One().Unmarshal(&params) != nil || params.Path == "" {
		return nil, errors.New("{ path: [string], recursive: [bool] }")
	}

	err := createDirectory(params.Path, params.Recursive)
	if err != nil {
		return nil, err
	}

	return true, nil
}

func Move(r *kite.Request) (interface{}, error) {
	var params struct {
		OldPath string
		NewPath string
	}

	if r.Args.One().Unmarshal(&params) != nil || params.OldPath == "" || params.NewPath == "" {
		return nil, errors.New("{ oldPath: [string], newPath: [string] }")
	}

	err := rename(params.OldPath, params.NewPath)
	if err != nil {
		return nil, err
	}

	return true, nil
}

func Copy(r *kite.Request) (interface{}, error) {
	var params struct {
		SrcPath string
		DstPath string
	}

	if r.Args.One().Unmarshal(&params) != nil || params.SrcPath == "" || params.DstPath == "" {
		return nil, errors.New("{ srcPath: [string], dstPath: [string] }")
	}

	err := cp(params.SrcPath, params.DstPath)
	if err != nil {
		return nil, err
	}

	return true, nil
}
