package entry

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/gofrs/flock"
)

// The small configuration manifest is outside the selected State so choosing a
// different State adapter cannot hide an existing binding. It is not task state.
func bindModules(opts Options) error {
	if err := os.MkdirAll(opts.Directory, 0700); err != nil {
		return err
	}
	lock := flock.New(filepath.Join(opts.Directory, "module-selection.lock"))
	ok, err := lock.TryLock()
	if err != nil {
		return err
	}
	defer lock.Close()
	if !ok {
		return errors.New("entry: module binding is busy")
	}
	desired := struct {
		Version, Workspace, Tenant, Employee, Owner, Backend, Profile string
		Modules                                                       ModuleSelection
	}{"entry-modules/1", opts.Profile.Workspace, opts.Profile.Tenant, opts.Profile.Employee, opts.Profile.Owner, opts.Profile.Backend, opts.Profile.ID, opts.Profile.Modules.Core()}
	path := filepath.Join(opts.Directory, "module-selection.json")
	raw, err := os.ReadFile(path)
	if err == nil {
		var old struct {
			Version, Workspace, Tenant, Employee, Owner, Backend, Profile string
			Modules                                                       ModuleSelection
		}
		if err = json.Unmarshal(raw, &old); err != nil {
			return err
		}
		if string(encode(old)) != string(encode(desired)) {
			return errors.New("entry: directory module binding changed; explicit migration required")
		}
		return nil
	}
	if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if opts.Profile.Modules.Core() != (ModuleSelection{}).Core() {
		if _, e := os.Stat(filepath.Join(opts.Directory, "state", "state.sqlite")); e == nil {
			return errors.New("entry: existing unmanifested builtin storage requires explicit module migration")
		} else if !errors.Is(e, os.ErrNotExist) {
			return e
		}
	}
	f, err := os.CreateTemp(opts.Directory, ".module-selection-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(encode(desired))
	}
	if err == nil {
		err = f.Sync()
	}
	err = errors.Join(err, f.Close())
	if err != nil {
		return err
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(opts.Directory)
	if err != nil {
		return err
	}
	err = errors.Join(d.Sync(), d.Close())
	if err != nil {
		return fmt.Errorf("entry: sync module binding: %w", err)
	}
	return nil
}
