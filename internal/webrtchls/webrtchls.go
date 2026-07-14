package webrtchls

/*
#cgo CFLAGS: -I${SRCDIR}/../../c-wrapper/include
#cgo LDFLAGS: -L${SRCDIR}/../../c-wrapper/lib -lwebrtc_hls -Wl,-rpath=${SRCDIR}/../../c-wrapper/lib
#include <stdlib.h>
#include <stdint.h>
#include "libwebrtc_hls.h"

// Forward declaration of the Go-exported trampoline.
// Note: no const qualifiers – CGo's generated export header omits them.
extern void goSegmentCallback(char* name, uint8_t* data, size_t size, int independent, void* userdata);

// Thin C wrapper so we can pass a real C function pointer to the library.
static inline int set_segment_callback_wrapper(webrtc_hls_session_t session,
                                                int register_cb,
                                                void* userdata) {
    webrtc_hls_segment_cb cb = register_cb ? (webrtc_hls_segment_cb)goSegmentCallback : NULL;
    return webrtc_hls_set_segment_callback(session, cb, userdata);
}
*/
import "C"
import (
	"errors"
	"sync"
	"sync/atomic"
	"unsafe"

	zlog "github.com/rs/zerolog/log"
)

// Error definitions
var (
	ErrGeneric       = errors.New("generic error occurred")
	ErrNotFound      = errors.New("requested resource not found")
	ErrAlreadyExists = errors.New("resource already exists")
	ErrInvalidParam  = errors.New("invalid parameter provided")
)

// LogLevel represents the log verbosity level for the C library.
type LogLevel int

const (
	LogLevelDebug LogLevel = 0
	LogLevelInfo  LogLevel = 1
	LogLevelWarn  LogLevel = 2
	LogLevelError LogLevel = 3
)

// SetLogLevel sets the minimum log level for the C library's internal logger.
// Messages below the specified level will be suppressed.
func SetLogLevel(level LogLevel) {
	C.webrtc_hls_set_loglevel(C.webrtc_hls_log_level_t(level))
}

// LogFormat represents the output format for the C library's internal logger.
type LogFormat int

const (
	LogFormatJSON LogFormat = 0 // Structured JSON output (default), suitable for log aggregation systems
	LogFormatText LogFormat = 1 // Human-readable text output, easier to read in a terminal
)

// SetLogFormat sets the output format for the C library's internal logger.
// JSON format is the default and is suitable for log aggregation systems (e.g. Loki, Elasticsearch).
// Text format is easier to read in a terminal.
func SetLogFormat(format LogFormat) {
	C.webrtc_hls_set_logformat(C.webrtc_hls_log_format_t(format))
}

// Init initializes the libwebrtc_hls library.
// This function must be called once before using any other library functions.
func Init() error {
	result := C.webrtc_hls_init()
	if result != C.WEBRTC_HLS_SUCCESS {
		return ErrGeneric
	}
	return nil
}

// Cleanup cleans up and releases all library resources.
// This function should be called once at program exit after all contexts and
// sessions have been destroyed.
func Cleanup() {
	C.webrtc_hls_cleanup()
}

// Context represents a WebRTC HLS context for managing sessions and Janus connections.
type Context struct {
	handle C.webrtc_hls_context_t
}

// NewContext creates a new context for managing sessions and Janus connections.
// Returns nil if context creation fails.
func NewContext() *Context {
	handle := C.webrtc_hls_context_create()
	if handle == nil {
		return nil
	}
	return &Context{handle: handle}
}

// Destroy destroys the context and frees all associated resources.
// All sessions within this context will be automatically destroyed.
func (c *Context) Destroy() {
	if c.handle != nil {
		C.webrtc_hls_context_destroy(c.handle)
		c.handle = nil
	}
}

// Session represents an HLS output session.
type Session struct {
	handle      C.webrtc_hls_session_t
	ctx         *Context
	segmentCBID uint64 // non-zero when a segment callback is registered
}

// SessionConfig contains configuration for creating a new session.
type SessionConfig struct {
	SessionID  string // Unique identifier for this session (required)
	OutputPath string // Output path for HLS manifest (e.g., "/path/to/output.m3u8") (required)
	EnableGst  bool   // Use GStreamer backend: false=FFmpeg, true=GStreamer

	// LL-HLS output parameters (ignored when EnableGst=true)
	VideoWidth         int     // Output video width in pixels (default: 540)
	VideoHeight        int     // Output video height in pixels (default: 960)
	VideoFPS           int     // Output video frame rate (default: 20)
	PartDurationSec    float64 // LL-HLS part target duration in seconds (default: 0.2)
	SegmentDurationSec float64 // HLS segment target duration in seconds (default: 1.0)
}

// CreateSession creates a new HLS output session.
// Each session represents one HLS stream that can contain multiple WebRTC participants.
// Returns nil if session creation fails.
func (c *Context) CreateSession(config *SessionConfig) (*Session, error) {
	if config == nil {
		return nil, ErrInvalidParam
	}

	cSessionID := C.CString(config.SessionID)
	defer C.free(unsafe.Pointer(cSessionID))

	cOutputPath := C.CString(config.OutputPath)
	defer C.free(unsafe.Pointer(cOutputPath))

	cConfig := C.webrtc_hls_session_config_t{
		session_id:           cSessionID,
		output_path:          cOutputPath,
		enable_gst:           C.int(0),
		video_width:          C.int(config.VideoWidth),
		video_height:         C.int(config.VideoHeight),
		video_fps:            C.int(config.VideoFPS),
		part_duration_sec:    C.double(config.PartDurationSec),
		segment_duration_sec: C.double(config.SegmentDurationSec),
	}
	if config.EnableGst {
		cConfig.enable_gst = C.int(1)
	}

	handle := C.webrtc_hls_session_create(c.handle, &cConfig)
	if handle == nil {
		return nil, ErrGeneric
	}

	return &Session{
		handle: handle,
		ctx:    c,
	}, nil
}

// Destroy destroys the session and stops HLS output.
// All WebRTC inputs will be automatically stopped and removed.
func (s *Session) Destroy() {
	zlog.Info().Msgf("[%s] Destroy: destroy session", s.segmentCBID)
	if s.handle != nil && s.ctx != nil && s.ctx.handle != nil {
		if s.segmentCBID != 0 {
			segmentCallbackMu.Lock()
			delete(segmentCallbackMap, s.segmentCBID)
			segmentCallbackMu.Unlock()
			s.segmentCBID = 0
		}
		zlog.Info().Msgf("[%s] Destroy: webrtc_hls_session_destroy", s.segmentCBID)
		C.webrtc_hls_session_destroy(s.ctx.handle, s.handle)
		s.handle = nil
	}
}

// InputConfig contains configuration for adding a WebRTC input.
type InputConfig struct {
	JanusRoomID         uint64 // Janus VideoRoom plugin room ID
	JanusSessionID      uint64 // Janus session ID for this participant
	JanusHandleID       uint64 // Janus handle ID for this participant
	JanusPublisherID    uint64 // Publisher ID within the Janus room
	JanusGatewayAddress string // Janus gateway address (e.g., "ws://localhost:8188")
	JanusAdminKey       string // Janus admin API key (default: "adminpwd")
	JanusAdminSecret    string // Janus admin secret (default: "admin")
	DisplayName         string // Human-readable display name for the participant
}

// AddInput adds a WebRTC input to the session.
// Connects to the specified Janus room and starts receiving audio/video from the publisher.
func (s *Session) AddInput(config *InputConfig) error {
	if config == nil {
		return ErrInvalidParam
	}

	cGatewayAddr := C.CString(config.JanusGatewayAddress)
	defer C.free(unsafe.Pointer(cGatewayAddr))

	cAdminKey := C.CString(config.JanusAdminKey)
	defer C.free(unsafe.Pointer(cAdminKey))

	cAdminSecret := C.CString(config.JanusAdminSecret)
	defer C.free(unsafe.Pointer(cAdminSecret))

	cDisplayName := C.CString(config.DisplayName)
	defer C.free(unsafe.Pointer(cDisplayName))

	cConfig := C.webrtc_hls_input_config_t{
		janus_room_id:         C.uint64_t(config.JanusRoomID),
		janus_session_id:      C.uint64_t(config.JanusSessionID),
		janus_handle_id:       C.uint64_t(config.JanusHandleID),
		janus_publisher_id:    C.uint64_t(config.JanusPublisherID),
		janus_gateway_address: cGatewayAddr,
		janus_admin_key:       cAdminKey,
		janus_admin_secret:    cAdminSecret,
		display_name:          cDisplayName,
	}

	result := C.webrtc_hls_add_input(s.ctx.handle, s.handle, &cConfig)
	return codeToError(result)
}

// RemoveInput removes a WebRTC input from the session.
// Disconnects the WebRTC peer connection and removes the input.
func (s *Session) RemoveInput(janusSessionID, janusHandleID uint64, displayName string) error {
	cDisplayName := C.CString(displayName)
	defer C.free(unsafe.Pointer(cDisplayName))

	result := C.webrtc_hls_remove_input(
		s.ctx.handle,
		s.handle,
		C.uint64_t(janusSessionID),
		C.uint64_t(janusHandleID),
		cDisplayName,
	)
	return codeToError(result)
}

// WriteID3Tag writes a custom ID3 tag to the HLS stream.
// This can be used to embed metadata that HLS players can read and process.
func (s *Session) WriteID3Tag(eventData, eventType string) error {
	cEventData := C.CString(eventData)
	defer C.free(unsafe.Pointer(cEventData))

	cEventType := C.CString(eventType)
	defer C.free(unsafe.Pointer(cEventType))

	result := C.webrtc_hls_write_id3_tag(s.handle, cEventData, cEventType)
	return codeToError(result)
}

// SetMute sets the mute status for a participant.
// Writes an ID3 "mute" tag to the HLS stream.
func (s *Session) SetMute(userID, clientID string, mute bool) error {
	cUserID := C.CString(userID)
	defer C.free(unsafe.Pointer(cUserID))

	cClientID := C.CString(clientID)
	defer C.free(unsafe.Pointer(cClientID))

	muteVal := C.int(0)
	if mute {
		muteVal = C.int(1)
	}

	result := C.webrtc_hls_set_mute(s.handle, cUserID, cClientID, muteVal)
	return codeToError(result)
}

// SetVideoOn sets the video on/off status for a participant.
// Writes an ID3 "video-on" tag to the HLS stream.
func (s *Session) SetVideoOn(userID, clientID string, videoOn bool) error {
	cUserID := C.CString(userID)
	defer C.free(unsafe.Pointer(cUserID))

	cClientID := C.CString(clientID)
	defer C.free(unsafe.Pointer(cClientID))

	videoVal := C.int(0)
	if videoOn {
		videoVal = C.int(1)
	}

	result := C.webrtc_hls_set_video_on(s.handle, cUserID, cClientID, videoVal)
	return codeToError(result)
}

// SessionInfo contains information about a session.
type SessionInfo struct {
	ParticipantCount int      // Number of active participants
	ParticipantNames []string // Display names of participants
}

// GetSessionInfo retrieves information about the session.
func (s *Session) GetSessionInfo() (*SessionInfo, error) {
	var cInfo C.webrtc_hls_session_info_t

	result := C.webrtc_hls_get_session_info(s.handle, &cInfo)
	if result != C.WEBRTC_HLS_SUCCESS {
		return nil, codeToError(result)
	}
	defer C.webrtc_hls_free_session_info(&cInfo)

	info := &SessionInfo{
		ParticipantCount: int(cInfo.participant_count),
		ParticipantNames: make([]string, 0, int(cInfo.participant_count)),
	}

	if cInfo.participant_count > 0 {
		// Convert C array of strings to Go slice
		namesSlice := (*[1 << 30]*C.char)(unsafe.Pointer(cInfo.participant_names))[:cInfo.participant_count:cInfo.participant_count]
		for i := 0; i < int(cInfo.participant_count); i++ {
			info.ParticipantNames = append(info.ParticipantNames, C.GoString(namesSlice[i]))
		}
	}

	return info, nil
}

// codeToError converts C return codes to Go errors.
func codeToError(code C.int) error {
	switch code {
	case C.WEBRTC_HLS_SUCCESS:
		return nil
	case C.WEBRTC_HLS_ERROR_NOT_FOUND:
		return ErrNotFound
	case C.WEBRTC_HLS_ERROR_ALREADY_EXISTS:
		return ErrAlreadyExists
	case C.WEBRTC_HLS_ERROR_INVALID_PARAM:
		return ErrInvalidParam
	default:
		return ErrGeneric
	}
}

// SegmentCallback is called whenever a new HLS segment or playlist is ready.
// name is the basename of the file ("init.mp4", "stream.m3u8", "part_00001.m4s", …).
// data is a copy of the raw bytes for that file.
// The callback is invoked from an internal muxer thread; avoid blocking operations.
type SegmentCallback func(name string, data []byte)

// segmentCallbackRegistry maps an integer handle to a Go SegmentCallback.
var (
	segmentCallbackMu      sync.RWMutex
	segmentCallbackMap     = make(map[uint64]SegmentCallback)
	segmentCallbackCounter uint64
)

//export goSegmentCallback
func goSegmentCallback(name *C.char, data *C.uint8_t, size C.size_t, independend C.int, userdata unsafe.Pointer) {
	id := uint64(uintptr(userdata))

	segmentCallbackMu.RLock()
	cb, ok := segmentCallbackMap[id]
	segmentCallbackMu.RUnlock()

	if !ok {
		return
	}

	goName := C.GoString(name)
	goData := C.GoBytes(unsafe.Pointer(data), C.int(size))
	cb(goName, goData)
}

// SetSegmentCallback registers a Go callback that fires whenever an HLS segment
// or playlist file is finalised by the muxer.
// Pass cb = nil to unregister the current callback.
func (s *Session) SetSegmentCallback(cb SegmentCallback) error {
	if s.handle == nil {
		return ErrInvalidParam
	}

	// Remove any previously registered callback for this session.
	if s.segmentCBID != 0 {
		segmentCallbackMu.Lock()
		delete(segmentCallbackMap, s.segmentCBID)
		segmentCallbackMu.Unlock()
		s.segmentCBID = 0
	}

	if cb == nil {
		result := C.set_segment_callback_wrapper(s.handle, C.int(0), nil)
		return codeToError(result)
	}

	id := atomic.AddUint64(&segmentCallbackCounter, 1)
	segmentCallbackMu.Lock()
	segmentCallbackMap[id] = cb
	segmentCallbackMu.Unlock()

	s.segmentCBID = id
	result := C.set_segment_callback_wrapper(s.handle, C.int(1), unsafe.Pointer(uintptr(id)))
	if err := codeToError(result); err != nil {
		segmentCallbackMu.Lock()
		delete(segmentCallbackMap, id)
		segmentCallbackMu.Unlock()
		s.segmentCBID = 0
		return err
	}
	return nil
}

// GetSegment retrieves a stored HLS segment by name.
// Recognised names: "init.mp4", "*.m3u8", "part_NNNNN.m4s".
// Returns ErrNotFound if no segment with that name is stored.
func (s *Session) GetSegment(name string) ([]byte, error) {
	if s.handle == nil || name == "" {
		return nil, ErrInvalidParam
	}

	cName := C.CString(name)
	defer C.free(unsafe.Pointer(cName))

	// First call: query required size.
	var outSize C.size_t
	result := C.webrtc_hls_get_segment(s.handle, cName, nil, 0, &outSize)
	if err := codeToError(result); err != nil {
		return nil, err
	}

	if outSize == 0 {
		return []byte{}, nil
	}

	buf := make([]byte, int(outSize))
	result = C.webrtc_hls_get_segment(
		s.handle,
		cName,
		(*C.uint8_t)(unsafe.Pointer(&buf[0])),
		outSize,
		&outSize,
	)
	if err := codeToError(result); err != nil {
		return nil, err
	}
	return buf[:int(outSize)], nil
}
