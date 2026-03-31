package server

import (
	"context"
	"time"

	pb "github.com/M1z23R/nikodis/pkg/gen"

	"github.com/M1z23R/nikodis/internal/cache"
)

type CacheService struct {
	pb.UnimplementedCacheServiceServer
	store *cache.Store
}

func NewCacheService(store *cache.Store) *CacheService {
	return &CacheService{store: store}
}

func (s *CacheService) Set(ctx context.Context, req *pb.SetRequest) (*pb.SetResponse, error) {
	ns := extractNamespace(ctx)
	key := namespacedKey(ns, req.Key)
	var ttl time.Duration
	if req.TtlSeconds != nil {
		ttl = time.Duration(*req.TtlSeconds) * time.Second
	}
	s.store.Set(key, req.Value, ttl)
	return &pb.SetResponse{}, nil
}

func (s *CacheService) Get(ctx context.Context, req *pb.GetRequest) (*pb.GetResponse, error) {
	ns := extractNamespace(ctx)
	key := namespacedKey(ns, req.Key)
	val, found := s.store.Get(key)
	return &pb.GetResponse{Value: val, Found: found}, nil
}

func (s *CacheService) BulkGet(ctx context.Context, req *pb.BulkGetRequest) (*pb.BulkGetResponse, error) {
	ns := extractNamespace(ctx)
	nsKeys := make([]string, len(req.Keys))
	keyMap := make(map[string]string, len(req.Keys))
	for i, key := range req.Keys {
		nk := namespacedKey(ns, key)
		nsKeys[i] = nk
		keyMap[nk] = key
	}
	found := s.store.BulkGet(nsKeys)
	entries := make([]*pb.BulkGetEntry, len(req.Keys))
	for i, key := range req.Keys {
		nk := namespacedKey(ns, key)
		val, ok := found[nk]
		entries[i] = &pb.BulkGetEntry{Key: key, Value: val, Found: ok}
	}
	return &pb.BulkGetResponse{Entries: entries}, nil
}

func (s *CacheService) Delete(ctx context.Context, req *pb.DeleteRequest) (*pb.DeleteResponse, error) {
	ns := extractNamespace(ctx)
	key := namespacedKey(ns, req.Key)
	deleted := s.store.Delete(key)
	return &pb.DeleteResponse{Deleted: deleted}, nil
}
