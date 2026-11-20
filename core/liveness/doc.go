// Package liveness implements node liveness detection, store-backed node
// records, and rate-limited eviction permits.
//
// Heartbeats and probe rounds are kept in memory as soft state; only confirmed
// state transitions and eviction permits are written to the store.
package liveness
