package trajectory

import (
	"bufio"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync"
)

// asyncSink 把流式字节交给后台 goroutine 落盘：热路径（tee 的 Read / Write）只做一次内存拷贝，
// 永远不会阻塞在 mkdir / open / write / flush 上。磁盘慢时数据暂存在内存队列中。
type asyncSink struct {
	path       string
	timingPath string // 为空则不记录分块时间线
	limit      int64

	mu     sync.Mutex
	cond   *sync.Cond
	queue  []sinkChunk
	closed bool
	n      int64 // 已接收的总字节数（含被截断部分）

	done   chan struct{}
	result sinkResult // done 关闭后只读
}

type sinkChunk struct {
	data []byte
	off  int64
	ms   int64
}

type sinkResult struct {
	bytes     int64
	truncated bool
	err       error
	wrote     bool // 数据文件已成功创建
}

func newAsyncSink(path, timingPath string, limit int64) *asyncSink {
	s := &asyncSink{path: path, timingPath: timingPath, limit: limit, done: make(chan struct{})}
	s.cond = sync.NewCond(&s.mu)
	go s.run()
	return s
}

// write 拷贝 p 入队；ms 为该块到达时距 attempt 开始的毫秒数。关闭后的写入被忽略。
func (s *asyncSink) write(p []byte, ms int64) {
	if s == nil || len(p) == 0 {
		return
	}
	data := make([]byte, len(p))
	copy(data, p)
	s.mu.Lock()
	if !s.closed {
		s.queue = append(s.queue, sinkChunk{data: data, off: s.n, ms: ms})
		s.n += int64(len(p))
		s.cond.Signal()
	}
	s.mu.Unlock()
}

// close 停止接收；后台写完剩余数据后关闭 done。
func (s *asyncSink) close() {
	if s == nil {
		return
	}
	s.mu.Lock()
	s.closed = true
	s.cond.Signal()
	s.mu.Unlock()
}

// wait 等待落盘结束并返回结果。
func (s *asyncSink) wait() sinkResult {
	if s == nil {
		return sinkResult{}
	}
	<-s.done
	return s.result
}

func (s *asyncSink) run() {
	defer close(s.done)
	data, err := createBodyFile(s.path, s.limit)
	var timingFile *os.File
	var timing *bufio.Writer
	if err == nil && s.timingPath != "" {
		if timingFile, err = os.OpenFile(s.timingPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644); err == nil {
			timing = bufio.NewWriterSize(timingFile, 16<<10)
		}
	}
	s.result.err = err
	s.result.wrote = data != nil
	line := make([]byte, 0, 64)
	for {
		s.mu.Lock()
		for len(s.queue) == 0 && !s.closed {
			s.cond.Wait()
		}
		batch := s.queue
		s.queue = nil
		closed := s.closed
		s.mu.Unlock()

		for _, c := range batch {
			data.write(c.data) // data 为 nil 时是空操作
			if timing != nil {
				line = append(line[:0], `{"off":`...)
				line = strconv.AppendInt(line, c.off, 10)
				line = append(line, `,"n":`...)
				line = strconv.AppendInt(line, int64(len(c.data)), 10)
				line = append(line, `,"t":`...)
				line = strconv.AppendInt(line, c.ms, 10)
				line = append(line, "}\n"...)
				_, _ = timing.Write(line)
			}
		}
		if closed {
			s.mu.Lock()
			leftover := len(s.queue)
			s.mu.Unlock()
			if leftover == 0 {
				break
			}
		}
	}
	if data != nil {
		s.result.err = errors.Join(s.result.err, data.close())
		s.result.truncated = data.truncated
	}
	if timing != nil {
		s.result.err = errors.Join(s.result.err, timing.Flush(), timingFile.Close())
	}
	s.mu.Lock()
	s.result.bytes = s.n
	s.mu.Unlock()
}

// sinkPaths 返回 attempt 目录下响应文件与时间线文件的路径。
func sinkPaths(dir, name string) (string, string) {
	return filepath.Join(dir, name), filepath.Join(dir, "upstream.timing.ndjson")
}
