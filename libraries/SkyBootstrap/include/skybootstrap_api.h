#pragma once

#include <stdint.h>

#ifdef _WIN32
#ifdef SKYBOOTSTRAP_BUILD
#define SKYBOOTSTRAP_API __declspec(dllexport)
#else
#define SKYBOOTSTRAP_API
#endif
#define SKYBOOTSTRAP_CALL __stdcall
#else
#define SKYBOOTSTRAP_API
#define SKYBOOTSTRAP_CALL
#endif

/*
 * Plugin ABI 4.
 *
 * Version 4 adds optional web-UI messages and plugin-pushed events. The first
 * two fields intentionally keep the version-3 layout, so older plugins remain
 * loadable. New plugins should reject hosts older than the API they require.
 */
#define SKYBOOTSTRAP_API_VERSION 4u

/* Maximum request and response size accepted by SkyPluginUiMessage. */
#define SKYBOOTSTRAP_UI_MESSAGE_LIMIT (4u * 1024u * 1024u)

typedef void (SKYBOOTSTRAP_CALL *SkyBootstrapLogFn)(const char *message);

/*
 * Queue one JSON event for this plugin's web UI. Pass api->event_context as
 * the first argument. The JSON string is copied before the call returns.
 * Returns 1 when the event was accepted and 0 when it was rejected or the
 * per-plugin queue is full.
 */
typedef int (SKYBOOTSTRAP_CALL *SkyBootstrapPostEventFn)(void *context,
                                                         const char *json);

typedef struct SkyBootstrapApi {
  uint32_t version;
  SkyBootstrapLogFn log;
  /* NULL when the host is older than ABI 4. */
  SkyBootstrapPostEventFn post_event;
  /* Opaque host-owned value to pass to post_event. */
  void *event_context;
} SkyBootstrapApi;

/* Required plugin export. Return non-zero to accept the API. */
typedef int (SKYBOOTSTRAP_CALL *SkyPluginInitFn)(const SkyBootstrapApi *api);

/* Optional plugin export, called before the DLL is unloaded. */
typedef void (SKYBOOTSTRAP_CALL *SkyPluginShutdownFn)(void);

/*
 * Optional plugin export used by the launcher's in-app web UI.
 *
 * request_json is a NUL-terminated UTF-8 JSON request. Write a NUL-terminated
 * UTF-8 JSON response into response_json and return its byte length (excluding
 * the terminator). An empty response is represented by returning 0. Return a
 * negative value to report an error. The host always provides a buffer of
 * SKYBOOTSTRAP_UI_MESSAGE_LIMIT bytes.
 */
typedef int32_t (SKYBOOTSTRAP_CALL *SkyPluginUiMessageFn)(
    const char *request_json, char *response_json, uint32_t response_capacity);

/*
 * Bootstrap exports. SkyLoader normally talks to the named pipe instead of
 * resolving these functions remotely.
 */
#ifdef __cplusplus
extern "C" {
#endif
SKYBOOTSTRAP_API int SKYBOOTSTRAP_CALL SkyBootstrapStart(void);
SKYBOOTSTRAP_API int SKYBOOTSTRAP_CALL SkyBootstrapLoadPluginW(const wchar_t *path);
SKYBOOTSTRAP_API int SKYBOOTSTRAP_CALL SkyBootstrapUnloadPluginW(const wchar_t *path);
SKYBOOTSTRAP_API uint32_t SKYBOOTSTRAP_CALL SkyBootstrapPluginCount(void);
SKYBOOTSTRAP_API uint32_t SKYBOOTSTRAP_CALL SkyBootstrapApiVersion(void);
#ifdef __cplusplus
}
#endif
