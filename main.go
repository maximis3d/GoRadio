package main

import (
	"bytes"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"sync"
	"time"
)

const (
	BUFFERSIZE = 8192
	DELAY      = 150 // ms
)

type Connection struct {
	bufferChannel chan []byte
}

type ConnectionPool struct {
	mu          sync.Mutex
	connections map[*Connection]struct{}
	bufferPool  sync.Pool
}

func NewConnectionPool() *ConnectionPool {
	return &ConnectionPool{
		connections: make(map[*Connection]struct{}),
		bufferPool: sync.Pool{
			New: func() interface{} {
				return make([]byte, BUFFERSIZE)
			},
		},
	}
}

func (cp *ConnectionPool) AddConnection(connection *Connection) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	cp.connections[connection] = struct{}{}
}

func (cp *ConnectionPool) DeleteConnection(connection *Connection) {
	cp.mu.Lock()
	defer cp.mu.Unlock()
	delete(cp.connections, connection)
}

func (cp *ConnectionPool) Broadcast(buffer []byte) {
	cp.mu.Lock()
	defer cp.mu.Unlock()

	for connection := range cp.connections {
		select {
		case connection.bufferChannel <- buffer:
		default: // If the buffer is full, we skip sending to avoid blocking
		}
	}
}

func stream(connectionPool *ConnectionPool, content []byte) {
	tempfile := bytes.NewReader(content)
	buffer := connectionPool.bufferPool.Get().([]byte) // Get a buffer from the pool
	defer connectionPool.bufferPool.Put(buffer)        // Ensure it's put back after use

	ticker := time.NewTicker(time.Millisecond * DELAY)
	defer ticker.Stop()

	for {
		// Read data into the buffer
		n, err := tempfile.Read(buffer)
		if err != nil {
			if err == io.EOF {
				break
			}
			log.Printf("Error reading from tempfile: %v", err)
			continue
		}

		// Broadcast the read buffer (only the portion that was read)
		connectionPool.Broadcast(buffer[:n])
		<-ticker.C // Wait for the ticker to tick before continuing
	}
}

func main() {
	fname := flag.String("filename", "file.aac", "path of the audio file")
	flag.Parse()

	file, err := os.Open(*fname)
	if err != nil {
		log.Fatal(err)
	}
	defer file.Close() // Ensure the file is closed after reading

	ctn, err := io.ReadAll(file)
	if err != nil {
		log.Fatal(err)
	}

	connPool := NewConnectionPool()

	go stream(connPool, ctn)

	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Add("Content-Type", "audio/aac")
		w.Header().Add("Connection", "keep-alive")

		flusher, ok := w.(http.Flusher)
		if !ok {
			log.Println("Could not create flusher")
			return
		}

		connection := &Connection{bufferChannel: make(chan []byte)}
		connPool.AddConnection(connection)
		defer connPool.DeleteConnection(connection) // Ensure connection is removed after handling

		log.Printf("%s has connected to the audio stream\n", r.Host)

		for {
			buf := <-connection.bufferChannel
			if _, err := w.Write(buf); err != nil {
				log.Printf("%s's connection to the audio stream has been closed: %v\n", r.Host, err)
				return
			}
			flusher.Flush()
		}
	})

	log.Println("Listening on port 8080...")
	log.Fatal(http.ListenAndServe(":8080", nil))
}
