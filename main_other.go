//go:build !windows

package main

// Development build for Linux/macOS: serves the same UI on http://127.0.0.1:8765
// and exposes the bindings over a tiny JSON-RPC + Server-Sent-Events bridge.

import (
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"reflect"
	"sync"
)

func main() {
	if err := loadConfig(); err != nil {
		log.Fatal(err)
	}
	var mu sync.Mutex
	var subs []chan string
	app := NewApp(func(event string, data any) {
		msg := jsonString(map[string]any{"event": event, "data": data})
		mu.Lock()
		for _, c := range subs {
			select {
			case c <- msg:
			default:
			}
		}
		mu.Unlock()
	})
	binds := bindings(app)

	bridge := `<script>
(function(){
  const names = ` + jsonString(keys(binds)) + `;
  for (const n of names) window[n] = (...args) => fetch('/rpc/'+n,{method:'POST',body:JSON.stringify(args)})
     .then(async r => { const j = await r.json(); if (j.error) throw j.error; return j.result; });
  const es = new EventSource('/events');
  es.onmessage = e => { const m = JSON.parse(e.data); window.__onEvent && window.__onEvent(m.event, m.data); };
})();
</script>`

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		fmt.Fprint(w, bridge+pageHTML())
	})
	http.HandleFunc("/events", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		c := make(chan string, 256)
		mu.Lock()
		subs = append(subs, c)
		mu.Unlock()
		fl := w.(http.Flusher)
		fl.Flush()
		for {
			select {
			case m := <-c:
				fmt.Fprintf(w, "data: %s\n\n", m)
				fl.Flush()
			case <-r.Context().Done():
				return
			}
		}
	})
	http.HandleFunc("/rpc/", func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Path[len("/rpc/"):]
		fn, ok := binds[name]
		var resp = map[string]any{}
		if !ok {
			resp["error"] = "unknown function"
		} else {
			var raw []json.RawMessage
			_ = json.NewDecoder(r.Body).Decode(&raw)
			v := reflect.ValueOf(fn)
			var args []reflect.Value
			for i := 0; i < v.Type().NumIn(); i++ {
				p := reflect.New(v.Type().In(i))
				if i < len(raw) {
					_ = json.Unmarshal(raw[i], p.Interface())
				}
				args = append(args, p.Elem())
			}
			out := v.Call(args)
			for _, o := range out {
				if e, ok := o.Interface().(error); ok {
					if e != nil {
						resp["error"] = e.Error()
					}
				} else if o.Type() != reflect.TypeOf((*error)(nil)).Elem() {
					resp["result"] = o.Interface()
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	})
	go app.StartupUpdate()
	log.Println("UI: http://127.0.0.1:8765")
	log.Fatal(http.ListenAndServe("127.0.0.1:8765", nil))
}

func keys(m map[string]any) []string {
	var k []string
	for n := range m {
		k = append(k, n)
	}
	return k
}
