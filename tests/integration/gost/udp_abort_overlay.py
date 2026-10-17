"""Layer stream abort onto the pinned halfclose/drain research overlay.

Uses only the standard library. Source and parent overlay files are read-only.
The binary overlay excludes test exports; contract_overlay adds them separately.
"""
import argparse
import hashlib
import json
from pathlib import Path

PIPE_SHA256 = '1d83f46e2699086be124b9f9db3b273b4b652f102b563e8b98fc70867590fba0'
ORIGINAL_SHA256 = {
    'internal/net/pipe.go': PIPE_SHA256,
    'listener/quic/conn.go': '4d197d0263e92dc67ad517fd5f4e630c6ff13087cac02f09b6063728e87a313e',
    'listener/quic/listener.go': '87e4c67f90a33645b2cdaff6eb727f4f57637c1ae1f31e60fb1db95ccf121189',
    'dialer/quic/conn.go': '76d232049e5319922c63f748e05c352831ed91e98c71a54d6a8f26ae985f2ab4',
    'connector/relay/conn.go': 'a6c26a20aad5efd2a47c30d248fa4ddd2d85a17910baa21d54a3f1ef0eabf8bf',
    'handler/relay/conn.go': '03e31884236af04fbb024065ca8d2bcc53dcf128be2d0ff60947e90a58f037aa',
    'internal/io/io.go': 'd17f5cdc01c1242168462a44c37207aed87d54f154060242a777054b8327d62d',
}
PARENT_SHA256 = dict(ORIGINAL_SHA256, **{
    'internal/net/pipe.go': 'cac4e769b35499787433a1bf8545aedfb2e6a598a07bd3397dcf8ddf3be29d26',
    'listener/quic/conn.go': '910e6b2758741e0999a4272665dc971de77de031ec726feb854149b43237588a',
    'listener/quic/listener.go': '0245a38d6990673b69c552b58fbcb1ef1c1575fc814887394478fa49cc7edbe4',
    'connector/relay/conn.go': 'bc4b051d2cbad3191f0aafb9d0ee1bbea7a55a8f75b1d637f2f33f0998def691',
    'handler/relay/conn.go': '04fb52ca6cfcbfd06e4de2052c57c88eaa0dac5961b802157fcc809517db63eb',
})

ADAPTER_FIELDS = '''
	sessionContext context.Context
	writeMu sync.Mutex
	stateMu sync.Mutex
	writeClosed bool
	aborted bool
	closed bool
'''
ADAPTER_METHODS = '''
// Context follows the multiplexed session, not the send side FIN of one stream.
func (c *quicConn) Context() context.Context { return c.sessionContext }

func (c *quicConn) Write(p []byte) (int, error) {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.stateMu.Lock()
	closed := c.aborted || c.closed || c.writeClosed
	c.stateMu.Unlock()
	if closed { return 0, net.ErrClosed }
	return c.Stream.Write(p)
}

func (c *quicConn) CloseRead() error {
	c.Stream.CancelRead(quic.StreamErrorCode(0x100))
	return nil
}

func (c *quicConn) CloseWrite() error {
	// quic-go Close must never overlap Write, or run after CancelWrite.
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.aborted || c.writeClosed { return nil }
	c.writeClosed = true
	return c.Stream.Close()
}

func (c *quicConn) Close() error {
	c.stateMu.Lock()
	if c.closed { c.stateMu.Unlock(); return nil }
	c.closed = true
	c.Stream.CancelRead(quic.StreamErrorCode(0x100))
	// Wake an overlapping writer before waiting for writeMu. A deadline does
	// not reset already accepted data; normal Close still sends a reliable FIN.
	if !c.aborted && !c.writeClosed { _ = c.Stream.SetWriteDeadline(time.Now()) }
	c.stateMu.Unlock()
	return c.CloseWrite()
}

// Abort is stream scoped. It never closes the shared QUIC session.
func (c *quicConn) Abort() error {
	c.stateMu.Lock()
	defer c.stateMu.Unlock()
	if c.aborted { return nil }
	c.aborted, c.closed = true, true
	c.Stream.CancelRead(quic.StreamErrorCode(0x100))
	c.Stream.CancelWrite(quic.StreamErrorCode(0x100))
	return nil
}
'''

PIPE_STATE = '''
// pipeAbortState serializes every deadline mutation with abort. An idle or
// half-close grace deadline must not overwrite the immediate abort deadline.
type pipeAbortState struct {
	mu sync.Mutex
	aborted bool
}

func (s *pipeAbortState) readDeadline(rw io.ReadWriteCloser, at time.Time) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.aborted { return false }
	_ = xio.SetReadDeadline(rw, at)
	return true
}

func (s *pipeAbortState) abort(conns ...io.ReadWriteCloser) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.aborted { return nil }
	s.aborted = true
	var failures []error
	for index, rw := range conns {
		if err := xio.Abort(rw); err == nil {
			continue
		} else if !errors.Is(err, xio.ErrUnsupported) {
			failures = append(failures, fmt.Errorf("pipe abort connection %d: %w", index, err))
		}
		at := time.Now()
		if d, ok := rw.(interface { SetDeadline(time.Time) error }); ok {
			if err := d.SetDeadline(at); err == nil {
				continue
			} else {
				failures = append(failures, fmt.Errorf("pipe abort deadline connection %d: %w", index, err))
			}
		}
		// Last resort after unsupported/failed abort and unavailable/failed deadline.
		if err := rw.Close(); err != nil {
			failures = append(failures, fmt.Errorf("pipe abort close connection %d: %w", index, err))
		}
	}
	return errors.Join(failures...)
}
'''

IO_ABORT = '''package io

import (
    "errors"
    "net"
)

// Abort reaches a stream cancellation method through established connection
// wrappers. A bare embedded net.Conn without UnwrapConn remains opaque.
func Abort(v any) error {
	for depth := 0; depth < 64 && v != nil; depth++ {
		if a, ok := v.(interface { Abort() error }); ok { return a.Abort() }
		if u, ok := v.(interface { UnwrapConn() net.Conn }); ok {
			v = u.UnwrapConn()
			continue
		}
		break
	}
	return ErrUnsupported
}

func (rwc *readWriteCloser) Abort() error {
	supported := true
	var failures []error
	for _, v := range []any{rwc.Reader, rwc.Writer, rwc.Closer} {
		if err := Abort(v); errors.Is(err, ErrUnsupported) {
			supported = false
		} else if err != nil { failures = append(failures, err) }
	}
	if len(failures) > 0 { return errors.Join(failures...) }
	if !supported { return ErrUnsupported }
	return nil
}
'''

def sha(data):
    return hashlib.sha256(data).hexdigest()

def abort_overlay(output: Path, local_x: Path, parent: dict) -> dict:
    source, output = local_x.resolve(), output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    replacements = {str(Path(k).resolve()): str(Path(v).resolve())
                    for k, v in parent['overlay']['Replace'].items()}
    inherited = dict(replacements)
    records = []

    def read_effective(rel):
        target = source / rel
        original = target.read_bytes()
        if sha(original) != ORIGINAL_SHA256[rel]:
            raise RuntimeError('upstream SHA256 gate failed: ' + rel)
        effective = Path(inherited.get(str(target), str(target)))
        data = effective.read_bytes()
        if sha(data) != PARENT_SHA256[rel]:
            raise RuntimeError('halfclose parent SHA256 gate failed: ' + rel)
        return data

    def save(rel, data, *, source_bytes=None, operations=None):
        target = source / rel
        path = output / rel.replace('/', '__')
        raw = data.encode('utf-8')
        path.write_bytes(raw)
        replacements[str(target.resolve())] = str(path.resolve())
        records.append({'path': rel, 'original_sha256': ORIGINAL_SHA256.get(rel),
                        'parent_sha256': sha(source_bytes) if source_bytes is not None else None,
                        'candidate_sha256': sha(raw), 'operations': operations or []})

    def exact(data, old, new, operations, label):
        count = data.count(old)
        if count != 1:
            raise RuntimeError(f'{label}: expected one anchor, found {count}')
        operations.append(label)
        return data.replace(old, new, 1)

    rel = 'internal/net/pipe.go'
    raw = read_effective(rel)
    s = raw.decode('utf-8')
    ops = []
    def patch(old, new, label):
        nonlocal s
        s = exact(s, old, new, ops, label)
    patch('"io"\n', '"errors"\n\t"fmt"\n\t"io"\n\t"sync"\n', 'import synchronization and cleanup error context')
    patch('defer cancel()\n\n\terrCh', 'defer cancel()\n\tstate := new(pipeAbortState)\n\tctxDone := ctx.Done()\n\n\terrCh', 'shared abort state')
    patch('pipeHalf(ctx, rw1, rw2, options.readTimeout)', 'pipeHalfManaged(ctx, state, rw1, rw2, options.readTimeout)', 'forward worker state')
    patch('pipeHalf(ctx, rw2, rw1, options.readTimeout)', 'pipeHalfManaged(ctx, state, rw2, rw1, options.readTimeout)', 'reverse worker state')
    patch('firstErr = err\n\t\t\t}', 'firstErr = err\n\t\t\t\tcancel()\n\t\t\t\tfirstErr = errors.Join(firstErr, state.abort(rw1, rw2))\n\t\t\t\tctxDone = nil\n\t\t\t}', 'abort both halves on first non EOF error')
    old = '''		case <-ctx.Done():
			forceClose(rw1, rw2)
			for completed < 2 {
				if err := <-errCh; firstErr == nil && err != nil {
					firstErr = err
				}
				completed++
			}
			if firstErr != nil {
				return firstErr
			}
			return ctx.Err()'''
    new = '''		case <-ctxDone:
			if firstErr == nil { firstErr = ctx.Err() }
			firstErr = errors.Join(firstErr, state.abort(rw1, rw2))
			ctxDone = nil'''
    patch(old, new, 'parent cancellation waits for both workers')
    patch('\n\treturn firstErr\n}', '\n\t// Workers have left Write before graceful final Close.\n\tforceClose(rw1, rw2)\n\treturn firstErr\n}', 'final close after workers')
    old = '''func pipeHalf(ctx context.Context, src, dst io.ReadWriteCloser, readTimeout time.Duration) error {
	defer func() {
		// 传输完成后执行TCP半关闭
		halfClose(src, dst)
	}()'''
    new = '''func pipeHalf(ctx context.Context, src, dst io.ReadWriteCloser, readTimeout time.Duration) error {
	return pipeHalfManaged(ctx, new(pipeAbortState), src, dst, readTimeout)
}

func pipeHalfManaged(ctx context.Context, state *pipeAbortState, src, dst io.ReadWriteCloser, readTimeout time.Duration) error {'''
    patch(old, new, 'remove error path half close')
    old = '''				if rd, ok := src.(interface{ SetReadDeadline(time.Time) error }); ok {
					rd.SetReadDeadline(time.Now().Add(readTimeout))
				}'''
    new = '''				if !state.readDeadline(src, time.Now().Add(readTimeout)) { return ctx.Err() }'''
    patch(old, new, 'serialize idle deadlines with abort')
    old = '\t\t\t// 读取数据\n\t\t\tnr, readErr := reader.Read(buf)\n\t\t\tif nr > 0 {\n\t\t\t\tnw, writeErr := dst.Write(buf[:nr])\n\t\t\t\tif writeErr != nil {\n\t\t\t\t\treturn writeErr\n\t\t\t\t}\n\t\t\t\tif nw != nr {\n\t\t\t\t\treturn io.ErrShortWrite\n\t\t\t\t}\n\t\t\t}\n\t\t\tif readErr != nil {\n\t\t\t\tif readErr != io.EOF {\n\t\t\t\t\treturn readErr\n\t\t\t\t}\n\t\t\t\treturn nil // 正常结束\n\t\t\t}\n'
    new = '''			// Deliver every byte before interpreting the accompanying read error.
			nr, readErr := reader.Read(buf)
			if nr > 0 {
				nw, writeErr := dst.Write(buf[:nr])
				if writeErr != nil { return writeErr }
				if nw != nr { return io.ErrShortWrite }
			}
			if readErr != nil {
				if readErr == io.EOF { return halfCloseManaged(state, src, dst) }
				return readErr
			}'''
    patch(old, new, 'preserve fixed final data and short write checks')
    patch('func halfClose(src, dst io.ReadWriteCloser) {', 'func halfClose(src, dst io.ReadWriteCloser) {\n\t_ = halfCloseManaged(new(pipeAbortState), src, dst)\n}\n\nfunc halfCloseManaged(state *pipeAbortState, src, dst io.ReadWriteCloser) error {', 'normal EOF only half close helper')
    patch('if err := cw.CloseWrite(); err == xio.ErrUnsupported {', 'if err := cw.CloseWrite(); errors.Is(err, xio.ErrUnsupported) {', 'wrapped unsupported half close fallback')
    patch('''		} else {
			// 设置半关闭超时''', '''		} else if err != nil {
			return err
		} else {
			// 设置半关闭超时''', 'propagate real half close errors before grace')
    patch('''			if rd, ok := dst.(interface{ SetReadDeadline(time.Time) error }); ok {
				rd.SetReadDeadline(time.Now().Add(tcpWaitTimeout))
			}''', '\t\t\tstate.readDeadline(dst, time.Now().Add(tcpWaitTimeout))', 'serialize half close grace deadline')
    patch('''	} else {
		dst.Close() // 不支持CloseWrite，完全关闭
	}
}''', '''	} else {
		dst.Close() // 不支持CloseWrite，完全关闭
	}
	return nil
}''', 'half close return')
    s += PIPE_STATE
    save(rel, s, source_bytes=raw, operations=ops)

    for rel in ('listener/quic/conn.go', 'dialer/quic/conn.go'):
        raw = read_effective(rel)
        s = raw.decode('utf-8')
        ops = []
        if rel.startswith('listener'):
            s = exact(s, '\t"context"\n', '\t"context"\n\t"sync"\n\t"time"\n', ops, 'adapter imports')
        else:
            s = exact(s, '\t"net"\n', '\t"net"\n\t"sync"\n', ops, 'adapter imports')
            s = exact(s, 'Stream: stream,', 'Stream: stream,\n\t\tsessionContext: session.session.Context(),', ops, 'dialer session lifecycle')
        fields = ADAPTER_FIELDS
        methods = ADAPTER_METHODS
        if rel.startswith('listener'):
            fields = fields.replace('\n\tsessionContext context.Context', '')
            methods = methods.replace('// Context follows the multiplexed session, not the send side FIN of one stream.\nfunc (c *quicConn) Context() context.Context { return c.sessionContext }\n', '')
        s = exact(s, '\t*quic.Stream\n', '\t*quic.Stream\n' + fields, ops, 'adapter close state')
        s += methods
        save(rel, s, source_bytes=raw, operations=ops)
    rel = 'listener/quic/listener.go'
    raw = read_effective(rel)
    ops = []
    s = raw.decode('utf-8')
    # Dropped streams must release both directions rather than retaining credit.
    if s.count('\t\t\tstream.Close()') != 2:
        raise RuntimeError('listener discard close anchors changed')
    s = s.replace('\t\t\tstream.Close()', '\t\t\t_ = conn.Abort()')
    ops.append('two stream discard branches use per stream abort')
    save(rel, s, source_bytes=raw, operations=ops)
    # Parent relay header flush and write serialization remain unchanged.
    for rel in ('connector/relay/conn.go', 'handler/relay/conn.go'):
        raw = read_effective(rel)
        ops = []
        methods = '''// UnwrapConn exposes only the transport to stream-scoped cancellation.
func (c *tcpConn) UnwrapConn() net.Conn { return c.Conn }

func (c *tcpConn) CloseRead() error {
    if cr, ok := c.Conn.(xio.CloseRead); ok { return cr.CloseRead() }
    return xio.ErrUnsupported
}

'''
        s = exact(raw.decode('utf-8'), 'func (c *tcpConn) CloseWrite() error {',
                  methods + 'func (c *tcpConn) CloseWrite() error {', ops, 'relay cancellation transparency')
        save(rel, s, source_bytes=raw, operations=ops)
    read_effective('internal/io/io.go')
    save('internal/io/cleanup_review_abort.go', IO_ABORT)
    result = {'overlay': {'Replace': replacements}, 'patches': records,
              'parent_candidate': parent.get('candidate', 'halfclose-or-drain'),
              'inherited_replacements': len(inherited),
              'hypothesis': 'errors and cancellation must abort affected streams and release both workers and stream credit',
              'scope': 'isolated QUIC research; no production or module-cache mutation'}
    (output / 'overlay.json').write_text(json.dumps(result['overlay'], indent=2) + '\n', encoding='utf-8')
    (output / 'provenance.json').write_text(json.dumps(result, indent=2) + '\n', encoding='utf-8')
    return result


def contract_overlay(output: Path, local_x: Path, candidate: dict) -> dict:
    """Add test factories only to contract tests; never GOST binaries."""
    output = output.resolve()
    output.mkdir(parents=True, exist_ok=True)
    replacements = dict(candidate['overlay']['Replace'])
    for side in ('listener', 'dialer'):
        rel = side + '/quic/cleanup_review_export.go'
        shim = '''package quic
import ("net"; "github.com/quic-go/quic-go")
func NewReviewConn(stream *quic.Stream, session *quic.Conn) net.Conn {
 return &quicConn{Stream:stream, sessionContext:session.Context(), laddr:session.LocalAddr(), raddr:session.RemoteAddr()}
}
'''
        target = output / rel.replace('/', '__')
        target.write_text(shim, encoding='utf-8', newline='\n')
        replacements[str((local_x / rel).resolve())] = str(target)
    for side in ('connector', 'handler'):
        rel = side + '/relay/cleanup_review_export.go'
        target = output / rel.replace('/', '__')
        target.write_text('package relay\nimport "net"\nfunc NewReviewTCPConn(conn net.Conn) net.Conn { return &tcpConn{Conn:conn} }\n', encoding='utf-8', newline='\n')
        replacements[str((local_x / rel).resolve())] = str(target)
    result = {'Replace': replacements}
    (output / 'overlay.json').write_text(json.dumps(result, indent=2) + '\n', encoding='utf-8')
    return result


if __name__ == '__main__':
    p = argparse.ArgumentParser(description=__doc__)
    p.add_argument('--source', type=Path, required=True)
    p.add_argument('--output', type=Path, required=True)
    p.add_argument('--parent', type=Path, required=True)
    a = p.parse_args()
    value = abort_overlay(a.output, a.source, json.loads(a.parent.read_text(encoding='utf-8')))
    print(json.dumps({'overlay': str(a.output / 'overlay.json'), 'patches': len(value['patches'])}))
