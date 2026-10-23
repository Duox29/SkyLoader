// skyplugin.h -- tiny header-only kit for SkyLoader sample plugins.
//
// It wraps the three skills a native plugin needs:
//   1. a small JSON value type with a parser and a writer,
//   2. host plumbing (init / shutdown / log / post_event),
//   3. one canonical way to answer UI messages with an envelope.
//
// Sample plugins include this header so their code stays focused on the data
// pattern being taught. A real plugin would copy it into its own repository.
// The ABI contract is defined in
// libraries/SkyBootstrap/include/skybootstrap_api.h.
#pragma once

#include <cmath>
#include <cstdint>
#include <cstdio>
#include <cstdlib>
#include <cstring>
#include <mutex>
#include <string>
#include <string_view>
#include <utility>
#include <vector>

#include "skybootstrap_api.h"

namespace skyplugin {

// The host looks exports up by name; keep them undecorated and stable.
#if defined(_WIN32)
#define SKYPLUGIN_EXPORT extern "C" __declspec(dllexport)
#else
#define SKYPLUGIN_EXPORT extern "C" __attribute__((visibility("default")))
#endif

// Largest JSON event accepted by the host's per-plugin queue. Larger events
// are dropped, so shrink the event or ask the UI to fetch via a UI message.
inline constexpr size_t kMaxEventBytes = 64 * 1024;

// --- JSON value -------------------------------------------------------------

// One JSON value. Numbers are stored as double, so integers larger than 2^53
// lose precision; treat such payload sizes as a reason to paginate instead.
class Value {
 public:
  enum class Type : uint8_t { Null, Boolean, Number, String, Array, Object };

  Type type = Type::Null;
  bool boolean = false;
  double number = 0;
  std::string string;
  std::vector<Value> items;                           // array elements
  std::vector<std::pair<std::string, Value>> members; // object fields in order

  // Scalars are constructed directly; Null is the default value.
  Value() = default;
  explicit Value(bool flag) { type = Type::Boolean; boolean = flag; }
  explicit Value(int count) { type = Type::Number; number = count; }
  explicit Value(long long count) { type = Type::Number; number = static_cast<double>(count); }
  explicit Value(double value) { type = Type::Number; number = value; }
  Value(const char* text) { type = Type::String; string = text; }
  explicit Value(std::string text) { type = Type::String; string = std::move(text); }

  static Value array() {
    Value out;
    out.type = Type::Array;
    return out;
  }
  static Value object() {
    Value out;
    out.type = Type::Object;
    return out;
  }

  bool isNull() const { return type == Type::Null; }
  bool isBoolean() const { return type == Type::Boolean; }
  bool isNumber() const { return type == Type::Number; }
  bool isString() const { return type == Type::String; }
  bool isArray() const { return type == Type::Array; }
  bool isObject() const { return type == Type::Object; }

  // Object field lookups. A missing key (or a lookup on a non-object) returns
  // a null value, so chained probes stay null-safe.
  bool contains(std::string_view key) const {
    if (type != Type::Object) return false;
    for (const auto& member : members) {
      if (member.first == key) return true;
    }
    return false;
  }
  const Value& at(std::string_view key) const {
    if (type == Type::Object) {
      for (const auto& member : members) {
        if (member.first == key) return member.second;
      }
    }
    return NullValue();
  }
  const Value& item(size_t index) const {
    if (type == Type::Array && index < items.size()) return items[index];
    return NullValue();
  }
  // Array length or object member count; zero for other types.
  size_t size() const {
    if (type == Type::Array) return items.size();
    if (type == Type::Object) return members.size();
    return 0;
  }
  // Typed getters with fallbacks, for forgiving request parsing.
  std::string stringOr(std::string_view key, std::string fallback) const {
    const Value& found = at(key);
    return found.isString() ? found.string : std::move(fallback);
  }
  double numberOr(std::string_view key, double fallback) const {
    const Value& found = at(key);
    return found.isNumber() ? found.number : fallback;
  }
  bool boolOr(std::string_view key, bool fallback) const {
    const Value& found = at(key);
    return found.isBoolean() ? found.boolean : fallback;
  }

  // Insert or replace one object member, preserving insertion order.
  void set(std::string key, Value value) {
    for (auto& member : members) {
      if (member.first == key) {
        member.second = std::move(value);
        return;
      }
    }
    members.emplace_back(std::move(key), std::move(value));
  }
  // Append one value to an array.
  void push(Value value) {
    if (type != Type::Array) return;
    items.push_back(std::move(value));
  }

 private:
  static const Value& NullValue() {
    static const Value value;
    return value;
  }
};

// Parse one JSON document. Returns false and fills `error` (when provided) on
// malformed input, trailing garbage, or nesting deeper than 64 levels.
inline bool parse(std::string_view text, Value* out, std::string* error = nullptr);

// Serialize any JSON value back to compact text.
inline std::string write(const Value& value);

// --- request/response envelope ----------------------------------------------

// Requests from the page look like  {"v":1,"action":"<name>", ...params}
// Replies look like  {"v":1,"ok":true,"data":<any>}                on success or
//                    {"v":1,"ok":false,"error":{"code","message"}}  on failure.
// Envelope errors are application-level: the page still gets HTTP 200 and can
// render them. A negative return from SkyPluginUiMessage is reserved for calls
// that never made it into the plugin (transport, parse, or capacity errors).

inline Value okReply(Value data) {
  Value reply = Value::object();
  reply.set("v", Value(1));
  reply.set("ok", Value(true));
  reply.set("data", std::move(data));
  return reply;
}

inline Value errorReply(std::string_view code, std::string_view message) {
  Value details = Value::object();
  details.set("code", Value(std::string(code)));
  details.set("message", Value(std::string(message)));
  Value reply = Value::object();
  reply.set("v", Value(1));
  reply.set("ok", Value(false));
  reply.set("error", std::move(details));
  return reply;
}

inline Value errorReply(std::string_view code, std::string_view message,
                        Value detail) {
  Value reply = errorReply(code, message);
  Value& error = reply.members.back().second;
  error.set("detail", std::move(detail));
  return reply;
}

// --- host plumbing -----------------------------------------------------------

// UI handlers are plain functions:  void handleRequest(const Value&, Value&).
// The host serializes UI messages per plugin, so no extra locking is needed to
// use one function pointer.
typedef void Handler(const Value& request, Value& response);

namespace detail {

inline const SkyBootstrapApi*& Api() {
  static const SkyBootstrapApi* api = nullptr;
  return api;
}

inline std::mutex& HandlerMutex() {
  static std::mutex mutex;
  return mutex;
}

inline Handler*& StoredHandler() {
  static Handler* handler = nullptr;
  return handler;
}

inline Handler* CopyHandler() {
  std::lock_guard<std::mutex> lock(HandlerMutex());
  return StoredHandler();
}

inline void LogRaw(const std::string& message) {
  const SkyBootstrapApi* api = Api();
  if (api != nullptr && api->log != nullptr) {
    api->log(message.c_str());
  }
}

}  // namespace detail

// Adopt the host API. Returns false to reject the load when the host is older
// than the version this kit requires; the host then unloads the DLL again.
inline bool init(const SkyBootstrapApi* api, const char* pluginName) {
  if (api == nullptr || api->version < SKYBOOTSTRAP_API_VERSION) return false;
  detail::Api() = api;
  std::string started = pluginName ? pluginName : "plugin";
  started += " initialized (ABI ";
  started += std::to_string(api->version);
  started += ')';
  detail::LogRaw(started);
  if (api->post_event == nullptr) {
    detail::LogRaw("host lacks post_event; plugin events will be dropped");
  }
  return true;
}

// Called before unload. Releases resources through the host while the host API
// pointer is still valid, then clears it for the rest of the shutdown path.
inline void shutdown() {
  {
    std::lock_guard<std::mutex> lock(detail::HandlerMutex());
    detail::StoredHandler() = nullptr;
  }
  detail::LogRaw("plugin shutting down");
  detail::Api() = nullptr;
}

// The host API currently in use, or nullptr before init and after shutdown.
inline const SkyBootstrapApi* api() { return detail::Api(); }

inline void log(const std::string& message) { detail::LogRaw(message); }

// Queue one JSON event for the page. Any JSON value works; objects with a
// "type" field read best on the wire. Returns false when the event was not
// queued (host too old, event malformed or oversized, queue full).
inline bool postEvent(const Value& event) {
  const std::string json = write(event);
  if (json.size() > kMaxEventBytes) {
    log("event dropped: larger than the " +
        std::to_string(kMaxEventBytes / 1024) + " KiB per-event limit");
    return false;
  }
  const SkyBootstrapApi* host = detail::Api();
  if (host == nullptr || host->post_event == nullptr) return false;
  return host->post_event(host->event_context, json.c_str()) == 1;
}

// Install the handler the bridge calls into.
inline void setHandler(Handler* handler) {
  std::lock_guard<std::mutex> lock(detail::HandlerMutex());
  detail::StoredHandler() = handler;
}

// Bridge trampoline: run inside SkyPluginUiMessage. Parses the request,
// dispatches to the installed handler with a fresh envelope, and serializes
// the response. The handler must fill the response (okReply/errorReply);
// leaving it null yields an UNKNOWN_ACTION envelope. Exceptions become a
// PLUGIN_ERROR envelope instead of a crash.
inline int32_t dispatch(const char* request_json, char* response_json,
                        uint32_t response_capacity) {
  if (response_json == nullptr || response_capacity == 0) return -1;

  std::string replyJson;
  Value request;
  if (request_json == nullptr || !parse(request_json, &request)) {
    detail::LogRaw("UI request was not valid JSON");
    replyJson = write(errorReply("BAD_REQUEST", "Request is not valid JSON."));
  } else {
    const std::string action = request.stringOr("action", "");
    Value response;
    bool handled = false;
    try {
      if (Handler* handler = detail::CopyHandler()) {
        handler(request, response);
        handled = !response.isNull();
      }
    } catch (const std::exception& error) {
      detail::LogRaw(std::string("handler failed: ") + error.what());
      response = errorReply("PLUGIN_ERROR", error.what());
    } catch (...) {
      detail::LogRaw("handler failed with an unknown exception");
      response = errorReply("PLUGIN_ERROR", "unknown exception");
    }
    if (!handled && response.isNull()) {
      response = errorReply(
          "UNKNOWN_ACTION",
          action.empty() ? "Missing action." : "Unknown action: " + action);
    }
    replyJson = write(response);
  }

  const size_t length = replyJson.size();
  if (length + 1 > response_capacity) return -1;
  std::memcpy(response_json, replyJson.c_str(), length + 1);
  return static_cast<int32_t>(length);
}

// --- JSON parser / writer ----------------------------------------------------

namespace detail {

inline void skipSpace(std::string_view text, size_t* offset) {
  while (*offset < text.size()) {
    const char c = text[*offset];
    if (c != ' ' && c != '\t' && c != '\n' && c != '\r') break;
    ++*offset;
  }
}

inline bool isDigit(char c) { return c >= '0' && c <= '9'; }

inline int hexDigit(char c) {
  if (c >= '0' && c <= '9') return c - '0';
  if (c >= 'a' && c <= 'f') return c - 'a' + 10;
  if (c >= 'A' && c <= 'F') return c - 'A' + 10;
  return -1;
}

inline void encodeUtf8(unsigned code, std::string* out) {
  if (code < 0x80) {
    out->push_back(static_cast<char>(code));
  } else if (code < 0x800) {
    out->push_back(static_cast<char>(0xC0 | (code >> 6)));
    out->push_back(static_cast<char>(0x80 | (code & 0x3F)));
  } else if (code < 0x10000) {
    out->push_back(static_cast<char>(0xE0 | (code >> 12)));
    out->push_back(static_cast<char>(0x80 | ((code >> 6) & 0x3F)));
    out->push_back(static_cast<char>(0x80 | (code & 0x3F)));
  } else {
    out->push_back(static_cast<char>(0xF0 | (code >> 18)));
    out->push_back(static_cast<char>(0x80 | ((code >> 12) & 0x3F)));
    out->push_back(static_cast<char>(0x80 | ((code >> 6) & 0x3F)));
    out->push_back(static_cast<char>(0x80 | (code & 0x3F)));
  }
}

inline bool parseString(std::string_view text, size_t* offset, std::string* out,
                        std::string* error) {
  ++*offset;  // opening quote
  out->clear();
  while (*offset < text.size()) {
    const unsigned char c = static_cast<unsigned char>(text[*offset]);
    if (c == '"') {
      ++*offset;
      return true;
    }
    if (c == '\\') {
      ++*offset;
      if (*offset >= text.size()) break;
      const char escape = text[*offset];
      switch (escape) {
        case '"': out->push_back('"'); ++*offset; continue;
        case '\\': out->push_back('\\'); ++*offset; continue;
        case '/': out->push_back('/'); ++*offset; continue;
        case 'b': out->push_back('\b'); ++*offset; continue;
        case 'f': out->push_back('\f'); ++*offset; continue;
        case 'n': out->push_back('\n'); ++*offset; continue;
        case 'r': out->push_back('\r'); ++*offset; continue;
        case 't': out->push_back('\t'); ++*offset; continue;
        case 'u': break;
        default:
          *error = "bad escape character";
          return false;
      }
      // \uXXXX, with surrogate pairs merged into one code point.
      int code = 0;
      for (int digit = 0; digit < 4; ++digit) {
        ++*offset;
        if (*offset >= text.size()) {
          *error = "truncated \\u escape";
          return false;
        }
        const int value = hexDigit(text[*offset]);
        if (value < 0) {
          *error = "bad hex digit in \\u escape";
          return false;
        }
        code = (code << 4) | value;
      }
      ++*offset;
      if (code >= 0xD800 && code <= 0xDBFF) {
        // High surrogate; require an immediately following low surrogate.
        if (*offset + 1 >= text.size() || text[*offset] != '\\' ||
            text[*offset + 1] != 'u') {
          *error = "unpaired high surrogate";
          return false;
        }
        *offset += 2;  // now on the first hex digit of the low surrogate
        int low = 0;
        for (int digit = 0; digit < 4; ++digit) {
          if (*offset >= text.size()) {
            *error = "truncated surrogate pair";
            return false;
          }
          const int value = hexDigit(text[*offset]);
          if (value < 0) {
            *error = "bad hex digit in surrogate pair";
            return false;
          }
          low = (low << 4) | value;
          ++*offset;
        }
        if (low < 0xDC00 || low > 0xDFFF) {
          *error = "unpaired high surrogate";
          return false;
        }
        code = 0x10000 + ((code - 0xD800) << 10) + (low - 0xDC00);
      } else if (code >= 0xDC00 && code <= 0xDFFF) {
        *error = "unpaired low surrogate";
        return false;
      }
      encodeUtf8(static_cast<unsigned>(code), out);
      continue;
    }
    if (c < 0x20) {
      *error = "unescaped control character in string";
      return false;
    }
    out->push_back(static_cast<char>(c));
    ++*offset;
  }
  *error = "unterminated string";
  return false;
}

inline bool parseNumber(std::string_view text, size_t* offset, Value* out,
                        std::string* error) {
  const size_t start = *offset;
  if (*offset < text.size() && text[*offset] == '-') ++*offset;
  bool digits = false;
  while (*offset < text.size() && isDigit(text[*offset])) {
    ++*offset;
    digits = true;
  }
  if (digits && *offset < text.size() && text[*offset] == '.') {
    ++*offset;
    bool fraction = false;
    while (*offset < text.size() && isDigit(text[*offset])) {
      ++*offset;
      fraction = true;
    }
    if (!fraction) {
      *error = "digit required after decimal point";
      return false;
    }
  } else if (!digits) {
    *error = "invalid number";
    return false;
  }
  if (*offset < text.size() && (text[*offset] == 'e' || text[*offset] == 'E')) {
    ++*offset;
    if (*offset < text.size() &&
        (text[*offset] == '+' || text[*offset] == '-')) {
      ++*offset;
    }
    bool exponent = false;
    while (*offset < text.size() && isDigit(text[*offset])) {
      ++*offset;
      exponent = true;
    }
    if (!exponent) {
      *error = "digit required in exponent";
      return false;
    }
  }
  // JSON forbids leading zeros: 01, -01, 007 are all invalid.
  {
    const size_t first = start + (text[start] == '-' ? 1 : 0);
    if (text[first] == '0' && first + 1 < *offset && isDigit(text[first + 1])) {
      *error = "numbers must not start with a zero digit";
      return false;
    }
  }

  // strtod is locale-sensitive; hosts run with the classic "C" locale, and the
  // grammar above guarantees a canonical token shape for the converter.
  char buffer[64];
  const size_t length = *offset - start;
  if (length >= sizeof(buffer)) {
    *error = "number too long";
    return false;
  }
  std::memcpy(buffer, text.data() + start, length);
  buffer[length] = '\0';
  char* end = nullptr;
  const double value = std::strtod(buffer, &end);
  if (end != buffer + length) {
    *error = "invalid number";
    return false;
  }
  *out = Value(value);
  return true;
}

inline bool parseValue(std::string_view text, size_t* offset, int depth,
                       Value* out, std::string* error);

inline bool parseObject(std::string_view text, size_t* offset, int depth,
                        Value* out, std::string* error) {
  ++*offset;  // opening brace
  *out = Value::object();
  skipSpace(text, offset);
  if (*offset < text.size() && text[*offset] == '}') {
    ++*offset;
    return true;
  }
  for (;;) {
    skipSpace(text, offset);
    if (*offset >= text.size() || text[*offset] != '"') {
      *error = "expected object key string";
      return false;
    }
    std::string keyText;
    if (!parseString(text, offset, &keyText, error)) return false;
    skipSpace(text, offset);
    if (*offset >= text.size() || text[*offset] != ':') {
      *error = "expected ':' after object key";
      return false;
    }
    ++*offset;
    Value member;
    if (!parseValue(text, offset, depth + 1, &member, error)) return false;
    out->members.emplace_back(std::move(keyText), std::move(member));
    skipSpace(text, offset);
    if (*offset >= text.size()) {
      *error = "unterminated object";
      return false;
    }
    if (text[*offset] == ',') {
      ++*offset;
      continue;
    }
    if (text[*offset] == '}') {
      ++*offset;
      return true;
    }
    *error = "expected ',' or '}' in object";
    return false;
  }
}

inline bool parseArray(std::string_view text, size_t* offset, int depth,
                       Value* out, std::string* error) {
  ++*offset;  // opening bracket
  *out = Value::array();
  skipSpace(text, offset);
  if (*offset < text.size() && text[*offset] == ']') {
    ++*offset;
    return true;
  }
  for (;;) {
    Value item;
    if (!parseValue(text, offset, depth + 1, &item, error)) return false;
    out->items.push_back(std::move(item));
    skipSpace(text, offset);
    if (*offset >= text.size()) {
      *error = "unterminated array";
      return false;
    }
    if (text[*offset] == ',') {
      ++*offset;
      continue;
    }
    if (text[*offset] == ']') {
      ++*offset;
      return true;
    }
    *error = "expected ',' or ']' in array";
    return false;
  }
}

inline bool parseValue(std::string_view text, size_t* offset, int depth,
                       Value* out, std::string* error) {
  if (depth > 64) {
    *error = "nesting is too deep";
    return false;
  }
  skipSpace(text, offset);
  if (*offset >= text.size()) {
    *error = "unexpected end of input";
    return false;
  }
  const char c = text[*offset];
  switch (c) {
    case '{':
      return parseObject(text, offset, depth, out, error);
    case '[':
      return parseArray(text, offset, depth, out, error);
    case '"': {
      std::string content;
      if (!parseString(text, offset, &content, error)) return false;
      *out = Value(std::move(content));
      return true;
    }
    case 't':
      if (text.compare(*offset, 4, "true") == 0) {
        *offset += 4;
        *out = Value(true);
        return true;
      }
      *error = "invalid literal";
      return false;
    case 'f':
      if (text.compare(*offset, 5, "false") == 0) {
        *offset += 5;
        *out = Value(false);
        return true;
      }
      *error = "invalid literal";
      return false;
    case 'n':
      if (text.compare(*offset, 4, "null") == 0) {
        *offset += 4;
        *out = Value();
        return true;
      }
      *error = "invalid literal";
      return false;
    default:
      if (c == '-' || isDigit(c)) return parseNumber(text, offset, out, error);
      *error = "unexpected character";
      return false;
  }
}

inline void writeNumber(double number, std::string* out) {
  char buffer[64] = {};
  if (std::floor(number) == number && std::fabs(number) <= 9007199254740992.0) {
    const long long integer = static_cast<long long>(number);
    std::snprintf(buffer, sizeof(buffer), "%lld", integer);
  } else {
    // Prefer the shortest print that still round-trips exactly.
    std::snprintf(buffer, sizeof(buffer), "%.15g", number);
    if (std::strtod(buffer, nullptr) != number) {
      std::snprintf(buffer, sizeof(buffer), "%.17g", number);
    }
  }
  *out += buffer;
}

inline void writeString(std::string_view text, std::string* out) {
  out->push_back('"');
  char escape[8];
  for (const char raw : text) {
    const unsigned char c = static_cast<unsigned char>(raw);
    switch (c) {
      case '"': *out += "\\\""; continue;
      case '\\': *out += "\\\\"; continue;
      case '\b': *out += "\\b"; continue;
      case '\f': *out += "\\f"; continue;
      case '\n': *out += "\\n"; continue;
      case '\r': *out += "\\r"; continue;
      case '\t': *out += "\\t"; continue;
      default: break;
    }
    if (c < 0x20) {
      std::snprintf(escape, sizeof(escape), "\\u%04x", c);
      *out += escape;
      continue;
    }
    out->push_back(raw);
  }
  out->push_back('"');
}

inline void writeValue(const Value& value, std::string* out) {
  switch (value.type) {
    case Value::Type::Null:
      *out += "null";
      break;
    case Value::Type::Boolean:
      *out += value.boolean ? "true" : "false";
      break;
    case Value::Type::Number:
      writeNumber(value.number, out);
      break;
    case Value::Type::String:
      writeString(value.string, out);
      break;
    case Value::Type::Array: {
      *out += '[';
      for (size_t index = 0; index < value.items.size(); ++index) {
        if (index != 0) *out += ',';
        writeValue(value.items[index], out);
      }
      *out += ']';
      break;
    }
    case Value::Type::Object: {
      *out += '{';
      for (size_t index = 0; index < value.members.size(); ++index) {
        if (index != 0) *out += ',';
        writeString(value.members[index].first, out);
        *out += ':';
        writeValue(value.members[index].second, out);
      }
      *out += '}';
      break;
    }
  }
}

}  // namespace detail

inline bool parse(std::string_view text, Value* out, std::string* error) {
  std::string localError;
  size_t offset = 0;
  Value parsed;
  bool ok = detail::parseValue(text, &offset, 0, &parsed, &localError);
  if (ok) {
    detail::skipSpace(text, &offset);
    if (offset != text.size()) {
      localError = "trailing characters after JSON value";
      ok = false;
    }
  }
  if (!ok) {
    if (error != nullptr) *error = std::move(localError);
    return false;
  }
  *out = std::move(parsed);
  return true;
}

inline std::string write(const Value& value) {
  std::string out;
  detail::writeValue(value, &out);
  return out;
}

}  // namespace skyplugin
