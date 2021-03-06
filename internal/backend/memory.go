// Copyright 2018 Francisco Souza. All rights reserved.
// Use of this source code is governed by a BSD-style
// license that can be found in the LICENSE file.

package backend

import (
	"errors"
	"fmt"
	"sync"
	"time"
)

// StorageMemory is an implementation of the backend storage that stores data in memory
type StorageMemory struct {
	buckets map[string]bucketInMemory
	mtx     sync.RWMutex
}

type bucketInMemory struct {
	Bucket
	activeObjects   []Object
	archivedObjects []Object
}

func newBucketInMemory(name string, versioningEnabled bool) bucketInMemory {
	return bucketInMemory{Bucket{name, versioningEnabled, time.Now()}, []Object{}, []Object{}}
}

func (bm *bucketInMemory) addObject(obj Object) {
	obj.Generation = getNewGenerationIfZero(obj.Generation)
	index := findObject(obj, bm.activeObjects, false)
	if index >= 0 {
		if bm.VersioningEnabled {
			bm.activeObjects[index].Deleted = time.Now().Format(time.RFC3339)
			bm.cpToArchive(bm.activeObjects[index])
		}
		bm.activeObjects[index] = obj
	} else {
		bm.activeObjects = append(bm.activeObjects, obj)
	}
}

func getNewGenerationIfZero(generation int64) int64 {
	if generation == 0 {
		return time.Now().UnixNano() / 1000
	}
	return generation
}

func (bm *bucketInMemory) deleteObject(obj Object, matchGeneration bool) {
	index := findObject(obj, bm.activeObjects, matchGeneration)
	if index < 0 {
		return
	}
	if bm.VersioningEnabled {
		obj.Deleted = time.Now().Format(time.RFC3339)
		bm.mvToArchive(obj)
	} else {
		bm.deleteFromObjectList(obj, true)
	}
}

func (bm *bucketInMemory) cpToArchive(obj Object) {
	bm.archivedObjects = append(bm.archivedObjects, obj)
}

func (bm *bucketInMemory) mvToArchive(obj Object) {
	bm.cpToArchive(obj)
	bm.deleteFromObjectList(obj, true)
}

func (bm *bucketInMemory) deleteFromObjectList(obj Object, active bool) {
	objects := bm.activeObjects
	if !active {
		objects = bm.archivedObjects
	}
	index := findObject(obj, objects, !active)
	objects[index] = objects[len(objects)-1]
	if active {
		bm.activeObjects = objects[:len(objects)-1]
	} else {
		bm.archivedObjects = objects[:len(objects)-1]
	}
}

// findObject looks for an object in the given list and return the index where it
// was found, or -1 if the object doesn't exist.
func findObject(obj Object, objectList []Object, matchGeneration bool) int {
	for i, o := range objectList {
		if matchGeneration && obj.ID() == o.ID() {
			return i
		}
		if !matchGeneration && obj.IDNoGen() == o.IDNoGen() {
			return i
		}
	}
	return -1
}

// NewStorageMemory creates an instance of StorageMemory
func NewStorageMemory(objects []Object) Storage {
	s := &StorageMemory{
		buckets: make(map[string]bucketInMemory),
	}
	for _, o := range objects {
		s.CreateBucket(o.BucketName, false)
		bucket := s.buckets[o.BucketName]
		bucket.addObject(o)
		s.buckets[o.BucketName] = bucket
	}
	return s
}

// CreateBucket creates a bucket
func (s *StorageMemory) CreateBucket(name string, versioningEnabled bool) error {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	bucket, err := s.getBucketInMemory(name)
	if err == nil {
		if bucket.VersioningEnabled != versioningEnabled {
			return fmt.Errorf("a bucket named %s already exists, but with different properties", name)
		}
		return nil
	}
	s.buckets[name] = newBucketInMemory(name, versioningEnabled)
	return nil
}

// ListBuckets lists buckets
func (s *StorageMemory) ListBuckets() ([]Bucket, error) {
	s.mtx.RLock()
	defer s.mtx.RUnlock()
	buckets := []Bucket{}
	for _, bucketInMemory := range s.buckets {
		buckets = append(buckets, Bucket{bucketInMemory.Name, bucketInMemory.VersioningEnabled, bucketInMemory.TimeCreated})
	}
	return buckets, nil
}

// GetBucket checks if a bucket exists
func (s *StorageMemory) GetBucket(name string) (Bucket, error) {
	s.mtx.RLock()
	defer s.mtx.RUnlock()
	bucketInMemory, err := s.getBucketInMemory(name)
	return Bucket{bucketInMemory.Name, bucketInMemory.VersioningEnabled, bucketInMemory.TimeCreated}, err
}

func (s *StorageMemory) getBucketInMemory(name string) (bucketInMemory, error) {
	if bucketInMemory, found := s.buckets[name]; found {
		return bucketInMemory, nil
	}
	return bucketInMemory{}, fmt.Errorf("no bucket named %s", name)
}

// CreateObject stores an object
func (s *StorageMemory) CreateObject(obj Object) error {
	s.mtx.Lock()
	defer s.mtx.Unlock()
	bucketInMemory, err := s.getBucketInMemory(obj.BucketName)
	if err != nil {
		bucketInMemory = newBucketInMemory(obj.BucketName, false)
	}
	bucketInMemory.addObject(obj)
	s.buckets[obj.BucketName] = bucketInMemory
	return nil
}

// ListObjects lists the objects in a given bucket with a given prefix and delimeter
func (s *StorageMemory) ListObjects(bucketName string, versions bool) ([]Object, error) {
	s.mtx.RLock()
	defer s.mtx.RUnlock()
	bucketInMemory, err := s.getBucketInMemory(bucketName)
	if err != nil {
		return []Object{}, err
	}
	if !versions {
		return bucketInMemory.activeObjects, nil
	}
	return append(bucketInMemory.activeObjects, bucketInMemory.archivedObjects...), nil
}

// GetObject get an object by bucket and name
func (s *StorageMemory) GetObject(bucketName, objectName string) (Object, error) {
	return s.GetObjectWithGeneration(bucketName, objectName, 0)
}

// GetObjectWithGeneration retrieves an specific version of the object
func (s *StorageMemory) GetObjectWithGeneration(bucketName, objectName string, generation int64) (Object, error) {
	s.mtx.RLock()
	defer s.mtx.RUnlock()
	bucketInMemory, err := s.getBucketInMemory(bucketName)
	if err != nil {
		return Object{}, err
	}
	matchGeneration := false
	obj := Object{BucketName: bucketName, Name: objectName}
	listToConsider := bucketInMemory.activeObjects
	if generation != 0 {
		matchGeneration = true
		obj.Generation = generation
		listToConsider = append(listToConsider, bucketInMemory.archivedObjects...)
	}
	index := findObject(obj, listToConsider, matchGeneration)
	if index < 0 {
		return obj, errors.New("object not found")
	}
	return listToConsider[index], nil
}

// DeleteObject deletes an object by bucket and name
func (s *StorageMemory) DeleteObject(bucketName, objectName string) error {
	obj, err := s.GetObject(bucketName, objectName)
	if err != nil {
		return err
	}
	s.mtx.Lock()
	defer s.mtx.Unlock()
	bucketInMemory, err := s.getBucketInMemory(bucketName)
	if err != nil {
		return err
	}
	bucketInMemory.deleteObject(obj, true)
	s.buckets[bucketName] = bucketInMemory
	return nil
}
