package main

// watashi v1.2.6-ws1: two switches for proving where memory goes, both off
// unless the environment turns them on.
//
//	WARP_PLUS_STATS=5m            one line in the journal every 5 minutes:
//	                              goroutines and heap figures
//	WARP_PLUS_PPROF=127.0.0.1:6061  net/http/pprof on that address only

import (
	"log"
	"net/http"
	_ "net/http/pprof"
	"os"
	"runtime"
	"time"
)

func init() {
	if addr := os.Getenv("WARP_PLUS_PPROF"); addr != "" {
		go func() {
			log.Printf("watashi: pprof on %s ended: %v", addr, http.ListenAndServe(addr, nil))
		}()
	}
	every, err := time.ParseDuration(os.Getenv("WARP_PLUS_STATS"))
	if err != nil || every <= 0 {
		return
	}
	go func() {
		var m runtime.MemStats
		t := time.NewTicker(every)
		defer t.Stop()
		for range t.C {
			runtime.ReadMemStats(&m)
			log.Printf("watashi stats: goroutines=%d heap_inuse=%dKiB heap_idle=%dKiB heap_released=%dKiB sys=%dKiB gc=%d",
				runtime.NumGoroutine(), m.HeapInuse>>10, m.HeapIdle>>10, m.HeapReleased>>10, m.Sys>>10, m.NumGC)
		}
	}()
}
