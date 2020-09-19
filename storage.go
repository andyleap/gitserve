package main

import (
	"bytes"
	"fmt"
	"io"
	"io/ioutil"
	"net/http"
	"path"
	"strings"

	"github.com/andyleap/go-s3"
	"github.com/go-git/go-git/v5/config"
	"github.com/go-git/go-git/v5/plumbing"
	"github.com/go-git/go-git/v5/plumbing/format/index"
	"github.com/go-git/go-git/v5/plumbing/format/objfile"
	"github.com/go-git/go-git/v5/plumbing/storer"
	"github.com/go-git/go-git/v5/storage"
)

type S3Storage struct {
	s3   s3.Client
	base string
}

var _ storage.Storer = &S3Storage{}
var _ storer.Storer = &S3Storage{}

// NewEncodedObject returns a new plumbing.EncodedObject, the real type
// of the object can be a custom implementation or the default one,
// plumbing.MemoryObject.
func (s *S3Storage) NewEncodedObject() plumbing.EncodedObject {
	return &plumbing.MemoryObject{}
}

func (s *S3Storage) ObjectPath(h plumbing.Hash) string {
	return path.Join(s.base, "obj", h.String())
}

// SetEncodedObject saves an object into the storage, the object should
// be create with the NewEncodedObject, method, and file if the type is
// not supported.
func (s *S3Storage) SetEncodedObject(p plumbing.EncodedObject) (plumbing.Hash, error) {
	buf := &bytes.Buffer{}
	ow := objfile.NewWriter(buf)
	ow.WriteHeader(p.Type(), p.Size())
	rc, err := p.Reader()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	_, err = io.Copy(ow, rc)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	err = ow.Close()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	hdrs := &http.Header{}
	hdrs.Set("Content-Type", "application/x-git-"+p.Type().String())
	upload, err := s.s3.NewUpload(s.ObjectPath(p.Hash()), hdrs)
	if err != nil {
		return plumbing.ZeroHash, err
	}
	err = upload.Write(buf.Bytes())
	if err != nil {
		return plumbing.ZeroHash, err
	}
	err = upload.Done()
	if err != nil {
		return plumbing.ZeroHash, err
	}
	return ow.Hash(), nil
}

// EncodedObject gets an object by hash with the given
// plumbing.ObjectType. Implementors should return
// (nil, plumbing.ErrObjectNotFound) if an object doesn't exist with
// both the given hash and object type.
//
// Valid plumbing.ObjectType values are CommitObject, BlobObject, TagObject,
// TreeObject and AnyObject. If plumbing.AnyObject is given, the object must
// be looked up regardless of its type.
func (s *S3Storage) EncodedObject(t plumbing.ObjectType, h plumbing.Hash) (plumbing.EncodedObject, error) {
	r, err := s.s3.Get(s.ObjectPath(h))
	if err != nil {
		if s3err, ok := err.(s3.Error); ok && s3err.Code == "NoSuchKey" {
			return nil, plumbing.ErrObjectNotFound
		}
		return nil, err
	}

	or, err := objfile.NewReader(r)
	if err != nil {
		return nil, err
	}
	rt, size, err := or.Header()
	if t != plumbing.AnyObject && rt != t {
		return nil, plumbing.ErrObjectNotFound
	}
	mo := &plumbing.MemoryObject{}
	mo.SetType(rt)
	mo.SetSize(size)
	_, err = io.Copy(mo, or)
	if err != nil {
		return nil, err
	}
	return mo, nil
}

// IterObjects returns a custom EncodedObjectStorer over all the object
// on the storage.
//
// Valid plumbing.ObjectType values are CommitObject, BlobObject, TagObject,
func (s *S3Storage) IterEncodedObjects(t plumbing.ObjectType) (storer.EncodedObjectIter, error) {
	panic("not implemented")
}

// HasEncodedObject returns ErrObjNotFound if the object doesn't
// exist.  If the object does exist, it returns nil.
func (s *S3Storage) HasEncodedObject(h plumbing.Hash) error {
	_, err := s.s3.Head(s.ObjectPath(h))
	if err != nil {
		return plumbing.ErrObjectNotFound
	}
	return nil
}

func (s *S3Storage) EncodedObjectSize(h plumbing.Hash) (int64, error) {
	o, err := s.s3.Head(s.ObjectPath(h))
	if err != nil {
		return 0, plumbing.ErrObjectNotFound
	}
	return int64(o.Size), nil
}

func (s *S3Storage) RefPath(r plumbing.ReferenceName) string {
	return path.Join(s.base, "ref", r.String())
}

func (s *S3Storage) SetReference(r *plumbing.Reference) error {
	hdrs := &http.Header{}
	hdrs.Set("Content-Type", "application/x-git-"+r.Type().String())
	err := s.s3.Put(s.RefPath(r.Name()), []byte(r.String()), hdrs)
	return err
}

// CheckAndSetReference sets the reference `new`, but if `old` is
// not `nil`, it first checks that the current stored value for
// `old.Name()` matches the given reference value in `old`.  If
// not, it returns an error and doesn't update `new`.
func (s *S3Storage) CheckAndSetReference(new *plumbing.Reference, old *plumbing.Reference) error {
	oldref, err := s.Reference(old.Name())
	if err != nil {
		return err
	}
	if oldref.String() != old.String() {
		return fmt.Errorf("References don't match %q != %q", oldref.String(), old.String())
	}
	return s.SetReference(new)
}

func (s *S3Storage) Reference(rname plumbing.ReferenceName) (*plumbing.Reference, error) {
	r, err := s.s3.Get(s.RefPath(rname))
	if err != nil {
		return nil, err
	}
	buf, err := ioutil.ReadAll(r)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(string(buf), " ")
	ref := plumbing.NewReferenceFromStrings(parts[0], parts[1])
	return ref, nil
}

type ReferenceIter struct {
	s  *S3Storage
	li *s3.ListIter
}

func (ri *ReferenceIter) Next() (*plumbing.Reference, error) {
	o, err := ri.li.Next()
	if err == io.EOF {
		return nil, io.EOF
	}
	r, err := ri.s.s3.Get(o.Key)
	if err != nil {
		return nil, err
	}
	buf, err := ioutil.ReadAll(r)
	if err != nil {
		return nil, err
	}
	parts := strings.Split(string(buf), " ")
	ref := plumbing.NewReferenceFromStrings(parts[0], parts[1])
	return ref, nil
}

func (ri *ReferenceIter) ForEach(cb func(*plumbing.Reference) error) error {
	defer ri.Close()
	for {
		obj, err := ri.Next()
		if err != nil {
			if err == io.EOF {
				return nil
			}
			return err
		}
		if err := cb(obj); err != nil {
			if err == storer.ErrStop {
				return nil
			}
			return err
		}
	}
}

func (ri *ReferenceIter) Close() {
}

func (s *S3Storage) IterReferences() (storer.ReferenceIter, error) {
	li := s.s3.ListIter(path.Join(s.base, "ref"))
	return &ReferenceIter{
		s:  s,
		li: li,
	}, nil
}

func (s *S3Storage) RemoveReference(r plumbing.ReferenceName) error {
	return s.s3.Delete(s.RefPath(r))
}

func (s *S3Storage) CountLooseRefs() (int, error) {
	return 0, nil
}

func (s *S3Storage) PackRefs() error {
	return nil
}

func (s *S3Storage) SetShallow(_ []plumbing.Hash) error {
	panic("not implemented") // TODO: Implement
}

func (s *S3Storage) Shallow() ([]plumbing.Hash, error) {
	panic("not implemented") // TODO: Implement
}

func (s *S3Storage) SetIndex(_ *index.Index) error {
	panic("not implemented") // TODO: Implement
}

func (s *S3Storage) Index() (*index.Index, error) {
	panic("not implemented") // TODO: Implement
}

func (s *S3Storage) Config() (*config.Config, error) {
	r, err := s.s3.Get(path.Join(s.base, "config"))
	if err != nil {
		if s3err, ok := err.(s3.Error); ok && s3err.Code == "NoSuchKey" {
			return config.NewConfig(), nil
		}
		return nil, err
	}
	c := config.NewConfig()
	buf, err := ioutil.ReadAll(r)
	if err != nil {
		return nil, err
	}
	err = c.Unmarshal(buf)
	if err != nil {
		return nil, err
	}
	return c, nil
}

func (s *S3Storage) SetConfig(c *config.Config) error {
	buf, err := c.Marshal()
	if err != nil {
		return err
	}
	err = s.s3.Put(path.Join(s.base, "config"), buf, nil)
	if err != nil {
		return err
	}
	return nil
}

// Module returns a Storer representing a submodule, if not exists returns a
// new empty Storer is returned
func (s *S3Storage) Module(name string) (storage.Storer, error) {
	panic("not implemented") // TODO: Implement
}
