package bundle

import (
	"archive/zip"
	"bytes"
	"fmt"
	"io/ioutil"
	"os"
	"path/filepath"
	"strings"

	"github.com/pkg/errors"
	"github.com/suborbital/reactr/directive"
)

// FileFunc is a function that returns the contents of a requested file
type FileFunc func(string) ([]byte, error)

// Bundle represents a Runnable bundle
type Bundle struct {
	filepath    string
	Directive   *directive.Directive
	Runnables   []WasmModuleRef
	staticFiles map[string]bool
}

// WasmModuleRef is a reference to a Wasm module (either its filepath or its bytes)
type WasmModuleRef struct {
	Filepath string
	Name     string
	data     []byte
}

// StaticFile returns a static file from the bundle, if it exists
func (b *Bundle) StaticFile(filePath string) ([]byte, error) {
	if _, exists := b.staticFiles[filePath]; !exists {
		return nil, os.ErrNotExist
	}

	r, err := zip.OpenReader(b.filepath)
	if err != nil {
		return nil, errors.Wrap(err, "failed to open bundle")
	}

	staticFilePath := ensurePrefix(filePath, "static/")

	var contents []byte

	for _, f := range r.File {
		if f.Name == staticFilePath {
			file, err := f.Open()
			if err != nil {
				return nil, errors.Wrap(err, "failed to Open static file")
			}

			contents, err = ioutil.ReadAll(file)
			if err != nil {
				return nil, errors.Wrap(err, "failed to ReadAll static file")
			}

			break
		}
	}

	return contents, nil
}

// Write writes a runnable bundle
// based loosely on https://golang.org/src/archive/zip/example_test.go
func Write(directive *directive.Directive, files []os.File, staticFiles []os.File, targetPath string) error {
	if directive == nil {
		return errors.New("directive must be provided")
	}

	// Create a buffer to write our archive to.
	buf := new(bytes.Buffer)

	// Create a new zip archive.
	w := zip.NewWriter(buf)

	// Add Directive to archive.
	if err := writeDirective(w, directive); err != nil {
		return errors.Wrap(err, "failed to writeDirective")
	}

	// Add some files to the archive.
	for _, file := range files {
		if file.Name() == "Directive.yaml" || file.Name() == "Directive.yml" {
			// only allow the canonical directive that's passed in
			continue
		}

		contents, err := ioutil.ReadAll(&file)
		if err != nil {
			return errors.Wrapf(err, "failed to read file %s", file.Name())
		}

		if err := writeFile(w, filepath.Base(file.Name()), contents); err != nil {
			return errors.Wrap(err, "failed to writeFile into bundle")
		}
	}

	// Add static files to the archive.
	for _, file := range staticFiles {
		contents, err := ioutil.ReadAll(&file)
		if err != nil {
			return errors.Wrapf(err, "failed to read file %s", file.Name())
		}

		fileName := fmt.Sprintf("static/%s", filepath.Base(file.Name()))
		if err := writeFile(w, fileName, contents); err != nil {
			return errors.Wrap(err, "failed to writeFile into bundle")
		}
	}

	if err := w.Close(); err != nil {
		return errors.Wrap(err, "failed to close bundle writer")
	}

	if err := ioutil.WriteFile(targetPath, buf.Bytes(), 0700); err != nil {
		return errors.Wrap(err, "failed to write bundle to disk")
	}

	return nil
}

func writeDirective(w *zip.Writer, directive *directive.Directive) error {
	directiveBytes, err := directive.Marshal()
	if err != nil {
		return errors.Wrap(err, "failed to Marshal Directive")
	}

	if err := writeFile(w, "Directive.yaml", directiveBytes); err != nil {
		return errors.Wrap(err, "failed to writeFile for Directive")
	}

	return nil
}

func writeFile(w *zip.Writer, name string, contents []byte) error {
	f, err := w.Create(name)
	if err != nil {
		return errors.Wrap(err, "failed to add file to bundle")
	}

	_, err = f.Write(contents)
	if err != nil {
		return errors.Wrap(err, "failed to write file into bundle")
	}

	return nil
}

// Read reads a .wasm.zip file and returns the bundle of wasm modules
// (suitable to be loaded into a wasmer instance)
func Read(path string) (*Bundle, error) {
	// Open a zip archive for reading.
	r, err := zip.OpenReader(path)
	if err != nil {
		return nil, errors.Wrap(err, "failed to open bundle")
	}

	defer r.Close()

	bundle := &Bundle{
		filepath:    path,
		Runnables:   []WasmModuleRef{},
		staticFiles: map[string]bool{},
	}

	// Iterate through the files in the archive,
	for _, f := range r.File {
		if f.Name == "Directive.yaml" {
			directive, err := readDirective(f)
			if err != nil {
				return nil, errors.Wrap(err, "failed to readDirective from bundle")
			}

			bundle.Directive = directive
			continue
		} else if strings.HasPrefix(f.Name, "static/") {
			// build up the list of available static files in the bundle for quick reference later
			filePath := strings.TrimPrefix(f.Name, "static/")
			bundle.staticFiles[filePath] = true
			continue
		} else if !strings.HasSuffix(f.Name, ".wasm") {
			continue
		}

		rc, err := f.Open()
		if err != nil {
			return nil, errors.Wrapf(err, "failed to open %s from bundle", f.Name)
		}

		defer rc.Close()

		wasmBytes, err := ioutil.ReadAll(rc)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to read %s from bundle", f.Name)
		}

		ref := refWithData(f.Name, wasmBytes)

		bundle.Runnables = append(bundle.Runnables, *ref)
	}

	if bundle.Directive == nil {
		return nil, errors.New("bundle did not contain directive")
	}

	return bundle, nil
}

func readDirective(f *zip.File) (*directive.Directive, error) {
	file, err := f.Open()
	if err != nil {
		return nil, errors.Wrapf(err, "failed to open %s from bundle", f.Name)
	}

	directiveBytes, err := ioutil.ReadAll(file)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to read %s from bundle", f.Name)
	}

	d := &directive.Directive{}
	if err := d.Unmarshal(directiveBytes); err != nil {
		return nil, errors.Wrap(err, "failed to Unmarshal Directive")
	}

	return d, nil
}

func refWithData(name string, data []byte) *WasmModuleRef {
	ref := &WasmModuleRef{
		Name: name,
		data: data,
	}

	return ref
}

// ModuleBytes returns the bytes for the module
func (w *WasmModuleRef) ModuleBytes() ([]byte, error) {
	if w.data == nil {
		if w.Filepath == "" {
			return nil, errors.New("missing Wasm module filepath in ref")
		}

		bytes, err := ioutil.ReadFile(w.Filepath)
		if err != nil {
			return nil, errors.Wrap(err, "failed to ReadFile for Wasm module")
		}

		w.data = bytes
	}

	return w.data, nil
}

func ensurePrefix(val, prefix string) string {
	if strings.HasPrefix(val, prefix) {
		return val
	}

	return fmt.Sprintf("%s%s", prefix, val)
}
