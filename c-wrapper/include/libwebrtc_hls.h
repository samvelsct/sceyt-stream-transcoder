#ifndef LIBWEBRTC_HLS_H
#define LIBWEBRTC_HLS_H

#include <stdint.h>
#include <stddef.h>

#ifdef __cplusplus
extern "C" {
#endif

// Export symbols explicitly when using -fvisibility=hidden
#if defined(__GNUC__) && __GNUC__ >= 4
#define WEBRTC_HLS_API __attribute__((visibility("default")))
#else
#define WEBRTC_HLS_API
#endif

/**
 * @file libwebrtc_hls.h
 * @brief Public C API for WebRTC to HLS transcoding library
 *
 * This library provides functionality to receive WebRTC streams via Janus Gateway
 * and output them as HLS streams with embedded metadata (ID3 tags).
 *
 * @par Thread Safety:
 * - webrtc_hls_init() and webrtc_hls_cleanup() must be called from the main thread
 * - Context and session operations are thread-safe
 * - Multiple contexts can be used concurrently
 *
 * @par Typical Usage:
 * 1. Call webrtc_hls_init() once at startup
 * 2. Create a context with webrtc_hls_context_create()
 * 3. Create one or more sessions with webrtc_hls_session_create()
 * 4. Add WebRTC inputs with webrtc_hls_add_input()
 * 5. Optionally track events with webrtc_hls_set_mute(), webrtc_hls_set_video_on()
 * 6. Remove inputs with webrtc_hls_remove_input()
 * 7. Destroy sessions with webrtc_hls_session_destroy()
 * 8. Destroy context with webrtc_hls_context_destroy()
 * 9. Call webrtc_hls_cleanup() once at shutdown
 */

/**
 * @defgroup return_codes Return Codes
 * @{
 */
#define WEBRTC_HLS_SUCCESS 0              /**< Operation completed successfully */
#define WEBRTC_HLS_ERROR (-1)               /**< Generic error occurred */
#define WEBRTC_HLS_ERROR_NOT_FOUND (-2)     /**< Requested resource not found */
#define WEBRTC_HLS_ERROR_ALREADY_EXISTS (-3) /**< Resource already exists */
#define WEBRTC_HLS_ERROR_INVALID_PARAM (-4)  /**< Invalid parameter provided */
/** @} */

/**
 * @defgroup opaque_types Opaque Handle Types
 * @{
 */
typedef void *webrtc_hls_context_t; /**< Opaque handle to a library context */
typedef void *webrtc_hls_session_t; /**< Opaque handle to an HLS session */
/** @} */

/**
 * @defgroup log_level Log Level
 * @{
 */

/**
 * @brief Log verbosity levels for the library's internal logger
 */
typedef enum {
    WEBRTC_HLS_LOG_DEBUG = 0, /**< Verbose debug messages */
    WEBRTC_HLS_LOG_INFO  = 1, /**< Informational messages */
    WEBRTC_HLS_LOG_WARN  = 2, /**< Warnings */
    WEBRTC_HLS_LOG_ERROR = 3, /**< Errors only */
} webrtc_hls_log_level_t;

/**
 * @brief Output format for the library's internal logger
 */
typedef enum {
    WEBRTC_HLS_LOG_FORMAT_JSON = 0, /**< Structured JSON output (default) */
    WEBRTC_HLS_LOG_FORMAT_TEXT = 1, /**< Human-readable text output */
} webrtc_hls_log_format_t;

/**
 * @brief Set the minimum log level for the library's internal logger
 *
 * Messages below the specified level will be suppressed. The default level
 * is WEBRTC_HLS_LOG_DEBUG (all messages are printed).
 *
 * @param level Minimum log level to display
 *
 * @note This controls only the custom Logger used throughout the library.
 *       FFmpeg and WebRTC subsystem logging are unaffected.
 * @note This function is thread-safe and may be called at any time.
 */
WEBRTC_HLS_API void webrtc_hls_set_loglevel(webrtc_hls_log_level_t level);

/**
 * @brief Set the output format for the library's internal logger
 *
 * Selects between structured JSON (default) and human-readable text output.
 * JSON format is suitable for log aggregation systems (e.g. Loki, Elasticsearch).
 * Text format is easier to read in a terminal.
 *
 * @param format Desired log output format
 *
 * @note This function is thread-safe and may be called at any time.
 */
WEBRTC_HLS_API void webrtc_hls_set_logformat(webrtc_hls_log_format_t format);

/** @} */

/**
 * Library initialization and cleanup
 */

/**
 * @brief Initialize the libwebrtc_hls library
 *
 * This function must be called once before using any other library functions.
 * It initializes the WebRTC subsystem and internal threading.
 *
 * @return WEBRTC_HLS_SUCCESS on success, WEBRTC_HLS_ERROR on failure
 *
 * @note This function is not thread-safe and should be called from the main thread
 * @see webrtc_hls_cleanup()
 */
WEBRTC_HLS_API int webrtc_hls_init(void);

/**
 * @brief Clean up and release all library resources
 *
 * This function should be called once at program exit after all contexts and
 * sessions have been destroyed. It cleans up the WebRTC subsystem and releases
 * internal resources.
 *
 * @note All contexts and sessions must be destroyed before calling this function
 * @note This function is not thread-safe and should be called from the main thread
 * @see webrtc_hls_init()
 */
WEBRTC_HLS_API void webrtc_hls_cleanup(void);

/**
 * Context management
 */

/**
 * @brief Create a new context for managing sessions and Janus connections
 *
 * A context is the top-level object that manages multiple sessions and Janus
 * gateway connections. It handles connection pooling and resource management
 * across sessions.
 *
 * @return Opaque context handle on success, NULL on failure
 *
 * @note Multiple contexts can exist simultaneously
 * @note The context must be freed with webrtc_hls_context_destroy()
 * @see webrtc_hls_context_destroy()
 */
WEBRTC_HLS_API webrtc_hls_context_t webrtc_hls_context_create(void);

/**
 * @brief Destroy a context and free all associated resources
 *
 * This function destroys all sessions within the context, closes all Janus
 * connections, and frees all allocated memory.
 *
 * @param ctx Context handle to destroy
 *
 * @note All sessions within this context will be automatically destroyed
 * @note After calling this function, the context handle becomes invalid
 * @see webrtc_hls_context_create()
 */
WEBRTC_HLS_API void webrtc_hls_context_destroy(webrtc_hls_context_t ctx);

/**
 * Session management - One session per HLS output stream
 */

/**
 * @brief Configuration structure for creating a new session
 */
typedef struct {
    const char *session_id; /**< Unique identifier for this session (required) */
    const char *output_path; /**< Output path for HLS manifest (e.g., "/path/to/output.m3u8") (required) */
    int enable_gst; /**< Use GStreamer backend: 0=FFmpeg, 1=GStreamer (ignored if not compiled with GST) */

    /* LL-HLS output parameters (ignored when enable_gst=1) */
    int video_width; /**< Output video width in pixels (default: 540) */
    int video_height; /**< Output video height in pixels (default: 960) */
    int video_fps; /**< Output video frame rate (default: 20) */
    double part_duration_sec; /**< LL-HLS part target duration in seconds (default: 0.2) */
    double segment_duration_sec; /**< HLS segment target duration in seconds (default: 1.0) */
    int playlist_window;
    int write_on_disk;
} webrtc_hls_session_config_t;

/**
 * @brief Create a new HLS output session
 *
 * Creates a session that manages WebRTC inputs and generates an HLS output stream.
 * Each session represents one HLS stream that can contain multiple WebRTC participants.
 * The output directory will be created if it doesn't exist.
 *
 * @param ctx Context handle that will own this session
 * @param config Session configuration (must not be NULL)
 * @return Session handle on success, NULL on failure
 *
 * @retval NULL if ctx or config is NULL
 * @retval NULL if session_id already exists
 * @retval NULL if output initialization fails
 *
 * @note The session_id must be unique within the context
 * @note The session must be freed with webrtc_hls_session_destroy()
 * @see webrtc_hls_session_destroy()
 */
WEBRTC_HLS_API webrtc_hls_session_t webrtc_hls_session_create(
    webrtc_hls_context_t ctx,
    const webrtc_hls_session_config_t *config
);

/**
 * @brief Destroy a session and stop HLS output
 *
 * Stops all active inputs, closes the HLS output, and frees all session resources.
 *
 * @param ctx Context handle that owns the session
 * @param session Session handle to destroy
 *
 * @note All WebRTC inputs will be automatically stopped and removed
 * @note After calling this function, the session handle becomes invalid
 * @see webrtc_hls_session_create()
 */
WEBRTC_HLS_API void webrtc_hls_session_destroy(
    webrtc_hls_context_t ctx,
    webrtc_hls_session_t session
);

/**
 * WebRTC input management
 */

/**
 * @brief Configuration structure for adding a WebRTC input
 */
typedef struct {
    uint64_t janus_room_id; /**< Janus VideoRoom plugin room ID */
    uint64_t janus_session_id; /**< Janus session ID for this participant */
    uint64_t janus_handle_id; /**< Janus handle ID for this participant */
    uint64_t janus_publisher_id; /**< Publisher ID within the Janus room */
    const char *janus_gateway_address; /**< Janus gateway address (e.g., "ws://localhost:8188" or "localhost:8188") */
    const char *janus_admin_key; /**< Janus admin API key (default: "adminpwd") */
    const char *janus_admin_secret; /**< Janus admin secret (default: "admin") */
    const char *display_name; /**< Human-readable display name for the participant */
} webrtc_hls_input_config_t;

/**
 * @brief Add a WebRTC input to a session
 *
 * Connects to the specified Janus room and starts receiving audio/video from
 * the publisher. The streams are added to the HLS output. An ID3 "add-input"
 * tag is written to the HLS stream with participant information.
 *
 * @param ctx Context handle
 * @param session Session handle to add the input to
 * @param config Input configuration (must not be NULL)
 * @return WEBRTC_HLS_SUCCESS on success, error code on failure
 *
 * @retval WEBRTC_HLS_SUCCESS Input added successfully
 * @retval WEBRTC_HLS_ERROR_INVALID_PARAM Invalid parameter (NULL pointer)
 * @retval WEBRTC_HLS_ERROR Failed to connect to Janus or initialize WebRTC
 *
 * @note Multiple inputs can be added to the same session
 * @note The library will automatically establish WebRTC peer connection
 * @note Janus gateway must be running and accessible
 * @see webrtc_hls_remove_input()
 */
WEBRTC_HLS_API int webrtc_hls_add_input(
    webrtc_hls_context_t ctx,
    webrtc_hls_session_t session,
    const webrtc_hls_input_config_t *config
);

/**
 * @brief Remove a WebRTC input from a session
 *
 * Disconnects the WebRTC peer connection and removes the input from the session.
 * An ID3 "remove-input" tag is written to the HLS stream with updated participant
 * information.
 *
 * @param ctx Context handle
 * @param session Session handle containing the input
 * @param janus_session_id Janus session ID of the input to remove
 * @param janus_handle_id Janus handle ID of the input to remove
 * @param display_name Display name of the participant (for tracking, can be NULL)
 * @return WEBRTC_HLS_SUCCESS on success, error code on failure
 *
 * @retval WEBRTC_HLS_SUCCESS Input removed successfully
 * @retval WEBRTC_HLS_ERROR_INVALID_PARAM Invalid parameter (NULL pointer)
 * @retval WEBRTC_HLS_ERROR_NOT_FOUND Input not found in session
 * @retval WEBRTC_HLS_ERROR General error during removal
 *
 * @note The input is identified by janus_session_id and janus_handle_id
 * @see webrtc_hls_add_input()
 */
WEBRTC_HLS_API int webrtc_hls_remove_input(
    webrtc_hls_context_t ctx,
    webrtc_hls_session_t session,
    uint64_t janus_session_id,
    uint64_t janus_handle_id,
    const char *display_name
);

/**
 * Event and metadata management - ID3 tags for HLS
 */

/**
 * @brief Write a custom ID3 tag to the HLS stream
 *
 * Writes an ID3v2 tag with custom event data to the HLS stream. This can be used
 * to embed metadata that HLS players can read and process. The tag is written to
 * the next HLS segment.
 *
 * @param session Session handle
 * @param event_data Event data string (typically JSON format, must not be NULL)
 * @param event_type Event type identifier (must not be NULL)
 * @return WEBRTC_HLS_SUCCESS on success, error code on failure
 *
 * @retval WEBRTC_HLS_SUCCESS ID3 tag written successfully
 * @retval WEBRTC_HLS_ERROR_INVALID_PARAM Invalid parameter (NULL pointer)
 * @retval WEBRTC_HLS_ERROR Failed to write tag
 *
 * @note Common event types: "add-input", "remove-input", "mute", "video-on", "custom-event"
 * @note Event data is typically in JSON format for easy parsing by clients
 * @see webrtc_hls_set_mute(), webrtc_hls_set_video_on()
 */
WEBRTC_HLS_API int webrtc_hls_write_id3_tag(
    webrtc_hls_session_t session,
    const char *event_data,
    const char *event_type
);

/**
 * @brief Set the mute status for a participant
 *
 * Writes an ID3 "mute" tag to the HLS stream indicating that a participant has
 * muted or unmuted their audio. This allows HLS players to display mute status.
 *
 * @param session Session handle
 * @param user_id User identifier string (must not be NULL)
 * @param client_id Client identifier string (must not be NULL)
 * @param mute Mute status: 1=muted, 0=unmuted
 * @return WEBRTC_HLS_SUCCESS on success, error code on failure
 *
 * @retval WEBRTC_HLS_SUCCESS Mute status tag written successfully
 * @retval WEBRTC_HLS_ERROR_INVALID_PARAM Invalid parameter (NULL pointer)
 * @retval WEBRTC_HLS_ERROR Failed to write tag
 *
 * @note The ID3 tag contains JSON: {"userId":"user_id/client_id","mute":true/false}
 * @see webrtc_hls_set_video_on(), webrtc_hls_write_id3_tag()
 */
WEBRTC_HLS_API int webrtc_hls_set_mute(
    webrtc_hls_session_t session,
    const char *user_id,
    const char *client_id,
    int mute
);

/**
 * @brief Set the video on/off status for a participant
 *
 * Writes an ID3 "video-on" tag to the HLS stream indicating that a participant has
 * enabled or disabled their video. This allows HLS players to display video status.
 *
 * @param session Session handle
 * @param user_id User identifier string (must not be NULL)
 * @param client_id Client identifier string (must not be NULL)
 * @param video_on Video status: 1=video on, 0=video off
 * @return WEBRTC_HLS_SUCCESS on success, error code on failure
 *
 * @retval WEBRTC_HLS_SUCCESS Video status tag written successfully
 * @retval WEBRTC_HLS_ERROR_INVALID_PARAM Invalid parameter (NULL pointer)
 * @retval WEBRTC_HLS_ERROR Failed to write tag
 *
 * @note The ID3 tag contains JSON: {"userId":"user_id/client_id","videoOn":true/false}
 * @see webrtc_hls_set_mute(), webrtc_hls_write_id3_tag()
 */
WEBRTC_HLS_API int webrtc_hls_set_video_on(
    webrtc_hls_session_t session,
    const char *user_id,
    const char *client_id,
    int video_on
);

/**
 * Session information and queries
 */

/**
 * @brief Structure containing session information
 */
typedef struct {
    int participant_count; /**< Number of active participants in the session */
    char **participant_names; /**< Array of display names (dynamically allocated) */
} webrtc_hls_session_info_t;

/**
 * @brief Get information about a session
 *
 * Retrieves information about the session including the number of participants
 * and their display names. The returned data must be freed with
 * webrtc_hls_free_session_info().
 *
 * @param session Session handle
 * @param info Pointer to info structure to fill (must not be NULL)
 * @return WEBRTC_HLS_SUCCESS on success, error code on failure
 *
 * @retval WEBRTC_HLS_SUCCESS Information retrieved successfully
 * @retval WEBRTC_HLS_ERROR_INVALID_PARAM Invalid parameter (NULL pointer)
 * @retval WEBRTC_HLS_ERROR Failed to retrieve information
 *
 * @note The info structure contains dynamically allocated memory
 * @note Always call webrtc_hls_free_session_info() to free the returned data
 * @see webrtc_hls_free_session_info()
 *
 * @par Example:
 * @code
 * webrtc_hls_session_info_t info;
 * if (webrtc_hls_get_session_info(session, &info) == WEBRTC_HLS_SUCCESS) {
 *     printf("Participants: %d\n", info.participant_count);
 *     for (int i = 0; i < info.participant_count; i++) {
 *         printf("  - %s\n", info.participant_names[i]);
 *     }
 *     webrtc_hls_free_session_info(&info);
 * }
 * @endcode
 */
WEBRTC_HLS_API int webrtc_hls_get_session_info(
    webrtc_hls_session_t session,
    webrtc_hls_session_info_t *info
);

/**
 * @brief Free memory allocated by webrtc_hls_get_session_info()
 *
 * Frees all dynamically allocated memory in the session info structure.
 * After calling this function, the info structure should not be used.
 *
 * @param info Pointer to info structure to free (can be NULL)
 *
 * @note It is safe to call this function with a NULL pointer
 * @note This function sets participant_count to 0 and participant_names to NULL
 * @see webrtc_hls_get_session_info()
 */
WEBRTC_HLS_API void webrtc_hls_free_session_info(webrtc_hls_session_info_t *info);

/**
 * Memory store / segment access
 *
 * These functions expose the in-memory HLS segment store that is used when the
 * session writes output via LLHLSOutputInterface.  Callers can either poll for
 * named segments or register a push callback that fires each time a segment is
 * finalised by the muxer.
 */

/**
 * @brief Callback invoked whenever a new HLS segment or playlist is ready.
 *
 * @param name      Basename of the file ("init.mp4", "stream.m3u8",
 *                  "part_00001.m4s", …).  Valid only for the duration of the
 *                  call; copy if you need it longer.
 * @param data      Pointer to the raw bytes.  Valid only for the duration of
 *                  the call; copy if you need them longer.
 * @param size      Number of bytes pointed to by @p data.
 * @param independent
 * @param userdata  The opaque pointer supplied to
 *                  webrtc_hls_set_segment_callback().
 */
typedef void (*webrtc_hls_segment_cb)(const char *name,
                                      const uint8_t *data,
                                      size_t size,
                                      int independent,
                                      void *userdata);

/**
 * @brief Register a callback that fires when each HLS segment is ready.
 *
 * The callback is called from the muxer's internal write thread immediately
 * after each virtual file (init segment, playlist, part, …) is finalised.
 * Avoid blocking or taking slow locks inside the callback.
 *
 * Pass @p cb = NULL to unregister a previously installed callback.
 *
 * @param session   Session handle.
 * @param cb        Callback function, or NULL to unregister.
 * @param userdata  Arbitrary pointer forwarded to every callback invocation.
 * @return WEBRTC_HLS_SUCCESS, or WEBRTC_HLS_ERROR_INVALID_PARAM if @p session
 *         is NULL or the session has no LL-HLS output.
 */
WEBRTC_HLS_API int webrtc_hls_set_segment_callback(
    webrtc_hls_session_t session,
    webrtc_hls_segment_cb cb,
    void *userdata
);

/**
 * @brief Retrieve a stored segment by name.
 *
 * Copies the current bytes for the named file into a caller-supplied buffer.
 * If @p buf is NULL the function returns the required size without copying.
 *
 * Recognised names:
 *   - "init.mp4"       – fMP4 initialisation segment
 *   - "*.m3u8"         – current HLS playlist
 *   - "part_NNNNN.m4s" – individual LL-HLS part
 *
 * @param session    Session handle.
 * @param name       Basename of the segment to retrieve.
 * @param buf        Buffer to write into, or NULL to query size only.
 * @param buf_size   Size of @p buf in bytes (ignored when @p buf is NULL).
 * @param out_size   Receives the number of bytes written (or required when
 *                   @p buf is NULL).  Must not be NULL.
 * @return WEBRTC_HLS_SUCCESS on success.
 * @retval WEBRTC_HLS_ERROR_INVALID_PARAM  @p session, @p name, or @p out_size
 *                                          is NULL.
 * @retval WEBRTC_HLS_ERROR_NOT_FOUND      No segment with that name is stored.
 * @retval WEBRTC_HLS_ERROR               @p buf is non-NULL but @p buf_size is
 *                                          too small.
 */
WEBRTC_HLS_API int webrtc_hls_get_segment(
    webrtc_hls_session_t session,
    const char *name,
    uint8_t *buf,
    size_t buf_size,
    size_t *out_size
);

#ifdef __cplusplus
}
#endif

#endif // LIBWEBRTC_HLS_H
