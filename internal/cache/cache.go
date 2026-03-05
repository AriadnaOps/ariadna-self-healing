/*
Package cache provides in-memory caching with eviction policies.

This package implements bounded caches for storing OTel telemetry data:
  - Metrics cache with LRU eviction
  - Logs cache with FIFO eviction
  - Traces cache with LRU eviction

All caches support:
  - Size limits (bytes) - prevents memory exhaustion
  - Entry count limits - alternative to size-based limits
  - TTL-based expiration - removes stale data automatically
  - Adaptive sizing under memory pressure - graceful degradation

This demonstrates several important patterns and data structures:
  - LRU Cache: Least Recently Used eviction (good for access patterns)
  - FIFO Queue: First In First Out eviction (good for time-series data)
  - Doubly-Linked List: O(1) insertion/deletion at any position
  - Hash Map + Linked List: O(1) access with ordering
  - Reader/Writer Mutex: Allows multiple concurrent readers

Key Go concepts taught here:
  - container/list package (doubly-linked list)
  - sync.RWMutex (read-write locks for concurrency)
  - Type assertions (interface{} to concrete type)
  - Pointer manipulation
  - Time-based logic
*/
package cache

import (
	"container/list" // Go's standard doubly-linked list implementation
	"sync"           // For sync.RWMutex (read-write lock)
	"time"           // For time.Time, time.Duration, time.Since()
)

// ============================================================================
// EVICTION POLICIES
// ============================================================================

// EvictionPolicy defines the cache eviction strategy
// Type alias pattern: creates named type for documentation and type safety
type EvictionPolicy string

const (
	// LRU evicts least recently used entries first
	// Algorithm: Keep track of access time, evict entries not accessed recently
	// Best for: Data with access patterns (hot keys accessed repeatedly)
	// Implementation: Linked list, move to front on access, evict from back
	// Time complexity: O(1) for all operations (access, insert, evict)
	LRU EvictionPolicy = "LRU"

	// FIFO evicts oldest entries first (First In, First Out)
	// Algorithm: Queue, evict entries in insertion order
	// Best for: Time-series data (logs, metrics - old data less useful)
	// Implementation: Linked list, add to back, evict from front
	// Time complexity: O(1) for all operations
	FIFO EvictionPolicy = "FIFO"
)

// ============================================================================
// CONFIGURATION
// ============================================================================

// Config defines cache configuration
// Plain struct (no yaml tags) - this is internal config, not from file
// Will be created from config.CacheSettingsConfig
type Config struct {
	// MaxSize is the maximum cache size in bytes
	// int64: Can represent up to ~9 exabytes (plenty for cache sizes)
	// When currentSize + newEntry > MaxSize, trigger eviction
	MaxSize int64

	// MaxAge is the maximum age of entries
	// time.Duration: Represents nanoseconds internally, can be 5*time.Minute
	// Entries older than this are considered stale and removed
	// Checked on access (Get) and during periodic Cleanup()
	MaxAge time.Duration

	// MaxEntries is the maximum number of entries
	// Alternative limit: sometimes count is easier to reason about than bytes
	// Example: "Keep last 10,000 log entries" regardless of size
	// Whichever limit hits first (size or count) triggers eviction
	MaxEntries int

	// EvictionPolicy is the eviction strategy
	// LRU or FIFO - determines which entry to remove when over limit
	EvictionPolicy EvictionPolicy
}

// DefaultConfig returns a default cache configuration
// Factory function: creates Config with sensible defaults
// Used when creating cache without explicit configuration
func DefaultConfig() Config {
	return Config{
		MaxSize:        50 * 1024 * 1024, // 50MB in bytes (50 * 1024 * 1024)
		MaxAge:         5 * time.Minute,  // 5 minutes
		MaxEntries:     100000,           // 100k entries
		EvictionPolicy: LRU,              // Least recently used
	}
}

// ============================================================================
// CACHE ENTRY
// ============================================================================

// Entry represents a cache entry
// This is what gets stored in the linked list
// Contains both the data and metadata about the entry
type Entry struct {
	// Key is the cache key (for lookup in map)
	// Duplicated here (also map key) for easier removal from map
	Key string

	// Value is the cached data
	// interface{}: Can store any type (metrics, logs, traces)
	// Type assertions needed when retrieving: value.(ActualType)
	Value interface{}

	// Size is the entry size in bytes
	// int64: matches MaxSize type
	// Used to track total cache size (sum of all entry sizes)
	// Caller provides this when calling Set() - must estimate size
	Size int64

	// CreatedAt is when this entry was created
	// time.Time: Go's timestamp type (includes timezone, precision)
	// Used for: TTL checks (is entry too old?)
	CreatedAt time.Time

	// AccessedAt is when this entry was last accessed
	// Updated by Get() calls (only for LRU policy)
	// LRU uses this to determine "least recently used"
	// FIFO doesn't update this (only CreatedAt matters)
	AccessedAt time.Time
}

// ============================================================================
// CACHE STRUCTURE
// ============================================================================

// Cache is a generic in-memory cache with eviction
//
// Data structure explanation:
//
//	This implements a classic "Hash Map + Doubly-Linked List" pattern
//	- Map provides O(1) lookup by key
//	- List maintains order (for LRU/FIFO)
//	- Combining both gives us O(1) for get, set, delete, evict
//
// list.Element allows O(1) remove for eviction.
//   - O(1) insert at front or back
//   - O(1) delete at any position (if you have the element pointer)
//   - O(1) move element to front (for LRU)
//   - These operations would be O(n) with a slice
//
// Thread safety:
//   - Uses sync.RWMutex for concurrent access
//   - Multiple readers can read simultaneously (RLock)
//   - Only one writer at a time (Lock)
//   - Writers block readers and other writers
type Cache struct {
	// Configuration
	config Config // Cache limits and policy

	// mu is a reader-writer mutex for thread-safe access
	// sync.RWMutex: allows multiple concurrent readers OR one writer
	// Multiple goroutines can Get() simultaneously without blocking
	mu sync.RWMutex

	// ========== Storage (the core data structures) ==========

	// items maps keys to list elements
	// map[string]*list.Element: key -> pointer to node in linked list
	// Why? Fast O(1) lookup by key
	// *list.Element: pointer to node in the doubly-linked list
	// The Element.Value contains our Entry struct
	items map[string]*list.Element

	// order is the doubly-linked list maintaining entry order
	// *list.List: Go's standard container/list (doubly-linked list)
	// For LRU: front = most recently used, back = least recently used
	// For FIFO: front = oldest, back = newest
	// Each node (Element) stores an *Entry
	order *list.List

	// ========== Tracking ==========

	// currentSize is the total size of all entries in bytes
	// int64: sum of all entry.Size values
	// Used to enforce MaxSize limit
	// Updated on every Set() and Delete()
	currentSize int64

	// ========== Metrics (for observability) ==========

	// hits counts successful Get() calls
	// int64: can count up to ~9 quintillion hits (won't overflow)
	// Incremented when Get() finds the key and entry is not expired
	hits int64

	// misses counts failed Get() calls
	// Incremented when Get() doesn't find key or entry is expired
	misses int64

	// evictions counts number of entries evicted
	// Incremented every time evictOne() removes an entry
	// High evictions = cache too small or very hot (high churn)
	evictions int64
}

// ============================================================================
// CACHE OPERATIONS
// ============================================================================

// New creates a new cache with the given configuration
//
// Factory function: named New() by convention
// Returns *Cache (pointer) because:
//  1. Cache is large (contains map and list)
//  2. We want to modify it (Set, Delete change internal state)
//  3. Methods use pointer receivers
func New(config Config) *Cache {
	return &Cache{
		config: config,                         // Copy config
		items:  make(map[string]*list.Element), // Create empty map
		order:  list.New(),                     // Create empty doubly-linked list
		// Other fields have zero values:
		// currentSize, hits, misses, evictions all start at 0
	}
}

// Get retrieves an entry from the cache
//
// Returns (interface{}, bool):
//   - First return: the cached value (or nil if not found)
//   - Second return: true if found, false otherwise
//
//
//
//	if ok { /* use value */ }
//
// Thread safety: Uses Lock (not RLock) because might modify list (MoveToFront)
func (c *Cache) Get(key string) (interface{}, bool) {
	// Lock for writing (even though reading) because we might modify (MoveToFront)
	// c.mu.Lock(): acquire exclusive lock, blocks other Lock() and RLock()
	c.mu.Lock()

	// defer c.mu.Unlock(): schedule unlock when function returns
	// defer ensures unlock happens even if function panics
	// LIFO order: defers execute in reverse order of declaration
	defer c.mu.Unlock()

	// Look up key in map
	// elem: value from map (or zero value if not found)
	// ok: true if key exists, false otherwise
	elem, ok := c.items[key]
	if !ok {
		// Key not found in cache
		c.misses++        // Increment miss counter
		return nil, false // Return nil value and false
	}

	// Key found! Extract the Entry from the list element
	// elem.Value is interface{}, need type assertion to *Entry
	// Type assertion: elem.Value.(*Entry)
	// Format: value.(Type) - asserts that value is of Type
	// Returns *Entry or panics if wrong type (safe here, we control the data)
	entry := elem.Value.(*Entry)

	// ========== Check TTL (Time To Live) ==========
	// Has this entry expired (too old)?
	// time.Since(t) returns time elapsed since t (same as time.Now().Sub(t))
	if time.Since(entry.CreatedAt) > c.config.MaxAge {
		// Entry is expired (older than MaxAge)
		c.removeElement(elem) // Remove from cache
		c.misses++            // Count as miss
		return nil, false     // Not found (expired = not found)
	}

	// ========== Update access time for LRU ==========
	// Only relevant for LRU policy (FIFO doesn't care about access patterns)
	if c.config.EvictionPolicy == LRU {
		// Update last access time to now
		entry.AccessedAt = time.Now()

		// Move element to front of list (most recently used position)
		// c.order.MoveToFront(elem): O(1) operation (doubly-linked list)
		// Why? LRU evicts from back, so keeping accessed items at front
		// prevents them from being evicted
		c.order.MoveToFront(elem)
	}

	// Cache hit!
	c.hits++                 // Increment hit counter
	return entry.Value, true // Return cached value and true
}

// Set adds or updates an entry in the cache
//
// Parameters:
//
//	key - Cache key (string)
//	value - Value to cache (any type via interface{})
//	size - Size of this entry in bytes (caller must estimate)
//
// Behavior:
//   - If key exists: update value and size
//   - If key doesn't exist: add new entry (may trigger eviction first)
//
// Thread safety: Uses Lock (exclusive access for modifications)
func (c *Cache) Set(key string, value interface{}, size int64) {
	c.mu.Lock()         // Acquire exclusive lock
	defer c.mu.Unlock() // Release lock when function returns

	// Capture current time (used for both CreatedAt and AccessedAt)
	now := time.Now()

	// ========== Check if key already exists (update case) ==========
	// elem, ok := map[key]: check if key is in map
	if elem, ok := c.items[key]; ok {
		// Key exists, update existing entry

		// Extract entry from element
		entry := elem.Value.(*Entry)

		// Update size tracking:
		// Subtract old size, will add new size later
		c.currentSize -= entry.Size

		// Update entry fields
		entry.Value = value    // New value
		entry.Size = size      // New size
		entry.AccessedAt = now // Update access time

		// Add new size to current size
		c.currentSize += size

		// For LRU: move updated entry to front (recently accessed)
		if c.config.EvictionPolicy == LRU {
			c.order.MoveToFront(elem)
		}
		// For FIFO: don't move (keep insertion order)

		return // Done with update, exit function
	}

	// ========== Key doesn't exist, adding new entry ==========

	// Check if we need to make room (evict old entries)
	// Pass newEntry size so eviction knows how much space is needed
	c.evictIfNeeded(size)

	// Create new Entry struct
	// &Entry{...}: create Entry and return pointer
	entry := &Entry{
		Key:        key,   // Cache key
		Value:      value, // Cached value
		Size:       size,  // Entry size in bytes
		CreatedAt:  now,   // Creation timestamp
		AccessedAt: now,   // Access timestamp (same as creation for new entry)
	}

	// Add to linked list based on eviction policy
	// var elem *list.Element: declare variable, will be assigned in if/else
	var elem *list.Element
	if c.config.EvictionPolicy == LRU {
		// LRU: add to front (most recently used position)
		// c.order.PushFront(entry): returns *list.Element (the new node)
		elem = c.order.PushFront(entry)
	} else {
		// FIFO: add to back (newest position)
		// c.order.PushBack(entry): returns *list.Element
		elem = c.order.PushBack(entry)
	}

	// Add to map (key -> list element)
	// This enables O(1) lookup: map gives us the element, element has the entry
	c.items[key] = elem

	// Update current size
	c.currentSize += size
}

// Delete removes an entry from the cache
//
// If key doesn't exist, does nothing (idempotent)
// Thread safety: Uses Lock for exclusive access
func (c *Cache) Delete(key string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Check if key exists
	if elem, ok := c.items[key]; ok {
		// Key exists, remove it
		c.removeElement(elem)
	}
	// If key doesn't exist, nothing to do (silently succeed)
}

// evictIfNeeded removes entries until there's room for a new entry
//
// Called before adding new entry to ensure limits aren't exceeded
// Evicts entries one by one until both size and count limits are satisfied
//
// Parameters:
//
//	newSize - Size of the new entry being added
//
// Note: This is a private method (lowercase name), only called internally
func (c *Cache) evictIfNeeded(newSize int64) {
	// ========== Evict for size limit ==========
	// Keep evicting while: current + new > max AND list not empty
	// c.order.Len() > 0: ensure there are entries to evict
	// Len() check prevents infinite loop when newSize > MaxSize.
	for c.currentSize+newSize > c.config.MaxSize && c.order.Len() > 0 {
		c.evictOne() // Evict one entry (reduces currentSize)
	}

	// ========== Evict for entry count limit ==========
	// Keep evicting while: count >= max
	// >= not > because we're about to add one more entry
	for c.order.Len() >= c.config.MaxEntries {
		c.evictOne()
	}
}

// evictOne removes one entry based on eviction policy
//
// For LRU: removes from back (least recently used)
// For FIFO: removes from front (oldest)
//
// Note: Called by evictIfNeeded(), not directly by users
func (c *Cache) evictOne() {
	// Determine which end of list to evict from
	// var elem *list.Element: declare pointer to element
	var elem *list.Element

	if c.config.EvictionPolicy == LRU {
		// LRU: evict from back of list
		// c.order.Back(): returns last element (or nil if empty)
		// Back = least recently used (because Get() moves to front)
		elem = c.order.Back()
	} else {
		// FIFO: evict from front of list
		// c.order.Front(): returns first element (or nil if empty)
		// Front = oldest entry (because new entries added to back)
		elem = c.order.Front()
	}

	// Check if element exists (list might be empty)
	// elem != nil: nil check (safer than assuming list has elements)
	if elem != nil {
		c.removeElement(elem) // Remove element from cache
		c.evictions++         // Increment eviction counter
	}
}

// removeElement removes an element from the cache
//
// Performs three operations:
//  1. Remove from map (so lookups fail)
//  2. Remove from list (so iteration doesn't see it)
//  3. Update size tracking (decrease currentSize)
//
// Parameters:
//
//	elem - Pointer to list element to remove
//
// Note: Private method, assumes lock is already held by caller
func (c *Cache) removeElement(elem *list.Element) {
	// Extract entry from element
	// Need entry to get Key (for map delete) and Size (for size tracking)
	entry := elem.Value.(*Entry)

	// Remove from map
	// delete(map, key): built-in function to remove key from map
	// If key doesn't exist, delete is a no-op (doesn't panic)
	delete(c.items, entry.Key)

	// Remove from linked list
	// c.order.Remove(elem): O(1) operation (doubly-linked list)
	// Element knows its neighbors, so removal is fast
	c.order.Remove(elem)

	// Update size tracking
	// Subtract this entry's size from total
	c.currentSize -= entry.Size
}

// Cleanup removes expired entries
//
// Called periodically (e.g., every minute) to clean up stale data
// TTL expiration is lazy (checked on Get), this is proactive cleanup
//
// Returns:
//
//	int - Number of entries removed
//
// EvictExpired runs in the background because some keys may never be Get again.
// Without cleanup, expired entries would stay in cache forever
//
// Thread safety: Uses Lock for exclusive access
func (c *Cache) Cleanup() int {
	c.mu.Lock()
	defer c.mu.Unlock()

	cleaned := 0      // Counter for removed entries
	now := time.Now() // Current time (compare against CreatedAt)

	// Iterate through all entries
	// Why? Removing elem during iteration would break the iteration
	// Solution: Save next pointer before removing current

	// var next *list.Element: declare outside loop
	var next *list.Element

	// for elem := start; condition; elem = increment
	// elem := c.order.Front(): start at first element
	// elem != nil: loop while element exists
	// elem = next: use pre-saved next (not elem.Next() which might be invalid)
	for elem := c.order.Front(); elem != nil; elem = next {
		// Save next pointer before potentially removing elem
		next = elem.Next() // elem.Next() returns next element or nil

		// Extract entry to check age
		entry := elem.Value.(*Entry)

		// Check if entry is expired
		// now.Sub(entry.CreatedAt): time elapsed since creation
		// Alternative to time.Since(entry.CreatedAt)
		if now.Sub(entry.CreatedAt) > c.config.MaxAge {
			// Entry is too old, remove it
			c.removeElement(elem)
			cleaned++ // Increment counter
		}
	}

	return cleaned // Return number of entries removed
}

// ============================================================================
// CACHE INSPECTION (Read-Only Methods)
// ============================================================================

// Size returns the current cache size in bytes
//
// Thread safety: Uses RLock (read lock)
// Multiple goroutines can call Size() concurrently
func (c *Cache) Size() int64 {
	// c.mu.RLock(): acquire read lock
	// Multiple readers can hold RLock simultaneously
	// RLock blocks if a writer holds Lock
	c.mu.RLock()
	defer c.mu.RUnlock() // Release read lock

	return c.currentSize // Simple read, no modification
}

// Len returns the number of entries in the cache
//
// Thread safety: Uses RLock
func (c *Cache) Len() int {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// len(map): built-in function, returns number of keys in map
	// len(c.items) == c.order.Len() (both represent same entries)
	// Using map is slightly faster than c.order.Len()
	return len(c.items)
}

// Stats returns cache statistics
//
// Returns snapshot of current cache state for monitoring/metrics
// Thread safety: Uses RLock (reading multiple fields)
func (c *Cache) Stats() Stats {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Return Stats struct with current values
	// Struct literal: Stats{field: value, ...}
	return Stats{
		Size:       c.currentSize,       // Current size in bytes
		MaxSize:    c.config.MaxSize,    // Maximum allowed size
		Entries:    len(c.items),        // Current entry count
		MaxEntries: c.config.MaxEntries, // Maximum allowed entries
		Hits:       c.hits,              // Total cache hits
		Misses:     c.misses,            // Total cache misses
		Evictions:  c.evictions,         // Total evictions
	}
}

// ============================================================================
// STATISTICS
// ============================================================================

// Stats contains cache statistics
// Plain struct (no methods except those defined below)
// Used for monitoring and metrics
type Stats struct {
	Size       int64 // Current size in bytes
	MaxSize    int64 // Maximum size in bytes
	Entries    int   // Current number of entries
	MaxEntries int   // Maximum number of entries
	Hits       int64 // Total cache hits
	Misses     int64 // Total cache misses
	Evictions  int64 // Total evictions
}

// HitRate returns the cache hit rate (0-1)
//
// Receiver: (s Stats) - value receiver (Stats, not *Stats)
// Called as: stats.HitRate()
//
// Returns:
//
//	float64 - Hit rate from 0.0 (0%) to 1.0 (100%)
//
// Formula: hits / (hits + misses)
// Example: 80 hits, 20 misses = 0.80 (80% hit rate)
func (s Stats) HitRate() float64 {
	// Calculate total accesses
	total := s.Hits + s.Misses

	// Avoid division by zero
	if total == 0 {
		return 0 // No accesses yet, return 0
	}

	// Calculate hit rate
	// float64(int): type conversion (cast int to float64)
	// Why? Division of ints truncates, we want decimal result
	return float64(s.Hits) / float64(total)
}

// UsagePercent returns the percentage of max size used
//
// Returns:
//
//	float64 - Usage percentage from 0.0 to 100.0
//
// Example: 30MB used, 50MB max = 60.0 (60%)
func (s Stats) UsagePercent() float64 {
	// Avoid division by zero
	if s.MaxSize == 0 {
		return 0
	}

	// Calculate percentage
	// Formula: (current / max) * 100
	return float64(s.Size) / float64(s.MaxSize) * 100
}

// ============================================================================
// CACHE MANAGEMENT (Administrative Operations)
// ============================================================================

// ReduceSize reduces the cache max size by the given percentage
//
// Use case: Adaptive sizing under memory pressure (spec: P8 Graceful Degradation)
// Example: ReduceSize(20) reduces max size by 20% (e.g., 50MB -> 40MB)
// If current size exceeds new max, evicts entries until under limit
//
// Parameters:
//
//	percent - Percentage to reduce (e.g., 20 for 20%)
//
// Thread safety: Uses Lock for exclusive access
func (c *Cache) ReduceSize(percent int) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Calculate reduction amount
	// Formula: maxSize * percent / 100
	// Example: 50MB * 20 / 100 = 10MB reduction
	// int64 arithmetic: all values converted to int64
	reduction := c.config.MaxSize * int64(percent) / 100

	// Apply reduction
	c.config.MaxSize -= reduction

	// Evict entries if current size exceeds new max
	// Keep evicting while over limit and entries exist
	for c.currentSize > c.config.MaxSize && c.order.Len() > 0 {
		c.evictOne()
	}
}

// Clear removes all entries from the cache
//
// Use case: Reset cache, emergency memory reclaim
// More efficient than deleting entries one by one
//
// Thread safety: Uses Lock for exclusive access
func (c *Cache) Clear() {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Recreate map (old one will be garbage collected)
	// make(map[K]V): creates new empty map
	// Faster than deleting all keys: delete(map, key) in loop
	c.items = make(map[string]*list.Element)

	// Recreate list (old one will be garbage collected)
	// list.New(): creates new empty doubly-linked list
	c.order = list.New()

	// Reset size to zero
	c.currentSize = 0

	// Note: Don't reset metrics (hits, misses, evictions)
	// Those are cumulative statistics since cache creation
}
