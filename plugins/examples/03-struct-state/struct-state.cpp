// struct-state.cpp -- SkyLoader sample plugin 03: nested structs + push events.
//
// Data shape: a nested JSON document (struct pattern) whose leaves are reached
// with dot paths, plus a background thread that pushes "tick" events so the
// page can watch data it never asked for:
//
//   document (returned whole by "get")
//   {
//     "meta":     { "schemaVersion": 1 },
//     "profile":  { "nickname": "Sky Runner", "candles": 120, "realm": "Isle of Dawn" },
//     "settings": { "theme": "dark", "autoCollect": true },
//     "schedule": [ { "when": "08:30", "what": "Candle run" } ]
//   }
//
//   request  {"v":1,"action":"get"}
//            {"v":1,"action":"get-path","path":"profile.candles"}
//            {"v":1,"action":"set-path","path":"profile.candles","value":250}
//            {"v":1,"action":"patch","fields":{"profile.candles":300}}
//            {"v":1,"action":"schedule-add","when":"22:00","what":"Wax run"}
//            {"v":1,"action":"schedule-remove","index":0}
//   reply    {"v":1,"ok":true,"data":{...}}
//            {"v":1,"ok":false,"error":{"code":"NOT_FOUND","message":"..."}}
//   event    {"type":"tick","seq":3,"uptime":15}
//
// Dot paths: "profile.candles" walks object keys; "schedule.0.when" uses a
// numeric component to index the array. Only the final component of an object
// may introduce a new key; arrays grow through schedule-add, never set-path.
// Guides: plugins/docs/04-data-patterns.md, plugins/docs/03-ui-bridge.md

#include "skyplugin.h"

#include <atomic>
#include <chrono>
#include <cmath>
#include <cstdint>
#include <cstdlib>
#include <cstring>
#include <mutex>
#include <string>
#include <thread>
#include <utility>
#include <vector>

namespace {

using skyplugin::Value;

constexpr size_t kMaxScheduleItems = 64;
constexpr size_t kMaxTextLength = 60;
constexpr int kTickSeconds = 5;

std::mutex g_mutex;               // guards g_doc
Value g_doc = Value::object();    // assigned in SkyPluginInit

std::atomic<bool> g_tickerRunning{false};
std::thread g_ticker;
std::atomic<int> g_tickSeq{0};

Value scheduleItem(const std::string& when, const std::string& what) {
  Value item = Value::object();
  item.set("when", Value(when));
  item.set("what", Value(what));
  return item;
}

Value defaults() {
  Value meta = Value::object();
  meta.set("schemaVersion", Value(1));

  Value profile = Value::object();
  profile.set("nickname", Value("Sky Runner"));
  profile.set("candles", Value(120));
  profile.set("realm", Value("Isle of Dawn"));

  Value settings = Value::object();
  settings.set("theme", Value("dark"));
  settings.set("autoCollect", Value(true));

  Value schedule = Value::array();
  schedule.push(scheduleItem("08:30", "Candle run"));
  schedule.push(scheduleItem("12:00", "Ash run"));

  Value doc = Value::object();
  doc.set("meta", std::move(meta));
  doc.set("profile", std::move(profile));
  doc.set("settings", std::move(settings));
  doc.set("schedule", std::move(schedule));
  return doc;
}

Value* scheduleOf(Value* doc) {
  for (auto& member : doc->members) {
    if (member.first == "schedule") return &member.second;
  }
  return nullptr;
}

// --- dot-path helpers ---------------------------------------------------------

// Split "profile.candles" or "schedule.0" into components. An empty path or an
// empty component fails.
bool splitPath(const std::string& path, std::vector<std::string>* components,
               std::string* error) {
  components->clear();
  if (path.empty()) {
    *error = "The dot path must not be empty.";
    return false;
  }
  size_t start = 0;
  for (;;) {
    const size_t dot = path.find('.', start);
    std::string part = path.substr(
        start, dot == std::string::npos ? std::string::npos : dot - start);
    if (part.empty()) {
      *error = "The dot path '" + path + "' has an empty component.";
      return false;
    }
    components->push_back(std::move(part));
    if (dot == std::string::npos) return true;
    start = dot + 1;
  }
}

// interprets one path component as an array index inside an array of `limit`
// items; fails when the component is not a number or falls outside.
bool numericComponent(const std::string& component, size_t limit, size_t* index) {
  if (component.empty()) return false;
  for (const char c : component) {
    if (c < '0' || c > '9') return false;
  }
  char buffer[32] = {};
  if (component.size() >= sizeof(buffer)) return false;
  std::memcpy(buffer, component.c_str(), component.size());
  const long long parsed = std::strtoll(buffer, nullptr, 10);
  if (parsed < 0 || parsed >= static_cast<long long>(limit)) return false;
  *index = static_cast<size_t>(parsed);
  return true;
}

// Walk all but the last component down into the document. Intermediate object
// keys must exist; intermediate array components must select an existing item.
Value* resolveParent(Value* doc, const std::vector<std::string>& components,
                     std::string* error) {
  Value* cursor = doc;
  for (size_t depth = 0; depth + 1 < components.size(); ++depth) {
    const std::string& component = components[depth];
    const std::string where = "path segment '" + component + "'";
    if (cursor->isObject()) {
      Value* next = nullptr;
      for (auto& member : cursor->members) {
        if (member.first == component) {
          next = &member.second;
          break;
        }
      }
      if (next == nullptr) {
        if (error != nullptr) *error = where + " names a missing key.";
        return nullptr;
      }
      cursor = next;
      continue;
    }
    if (cursor->isArray()) {
      size_t index = 0;
      if (!numericComponent(component, cursor->items.size(), &index)) {
        if (error != nullptr) {
          *error = where + " must be a number that indexes one of the " +
                   std::to_string(cursor->items.size()) + " items.";
        }
        return nullptr;
      }
      cursor = &cursor->items[index];
      continue;
    }
    if (error != nullptr) {
      *error = where + " cannot be crossed: the value there is a scalar.";
    }
    return nullptr;
  }
  return cursor;
}

// Walk the full path in a read-only document and return the leaf, or nullptr.
const Value* resolveRead(const Value& doc,
                         const std::vector<std::string>& components,
                         std::string* error) {
  const Value* cursor = &doc;
  for (const std::string& component : components) {
    const std::string where = "path segment '" + component + "'";
    if (cursor->isObject()) {
      const Value& next = cursor->at(component);
      if (next.isNull()) {
        if (error != nullptr) *error = where + " names a missing key.";
        return nullptr;
      }
      cursor = &next;
      continue;
    }
    if (cursor->isArray()) {
      size_t index = 0;
      if (!numericComponent(component, cursor->items.size(), &index)) {
        if (error != nullptr) {
          *error = where + " must be a number that indexes one of the " +
                   std::to_string(cursor->items.size()) + " items.";
        }
        return nullptr;
      }
      cursor = &cursor->items[index];
      continue;
    }
    if (error != nullptr) {
      *error = where + " cannot be crossed: the value there is a scalar.";
    }
    return nullptr;
  }
  return cursor;
}

// Replace the leaf of a validated path. Object keys may be created; array
// components may only replace an existing item.
bool writeLeaf(Value* parent, const std::string& lastComponent, Value value,
               std::string* error) {
  if (parent->isObject()) {
    parent->set(lastComponent, std::move(value));
    return true;
  }
  if (parent->isArray()) {
    size_t index = 0;
    if (!numericComponent(lastComponent, parent->items.size(), &index)) {
      if (error != nullptr) {
        *error = "array leaf '" + lastComponent +
                 "' does not point to an existing item.";
      }
      return false;
    }
    parent->items[index] = std::move(value);
    return true;
  }
  if (error != nullptr) {
    *error = "the path ends at a scalar value, which cannot be replaced.";
  }
  return false;
}

// --- actions ------------------------------------------------------------------

void handleRequest(const Value& request, Value& response) {
  const std::string action = request.stringOr("action", "");
  std::lock_guard<std::mutex> lock(g_mutex);
  std::string error;

  if (action == "get") {
    response = skyplugin::okReply(g_doc);
    return;
  }

  if (action == "get-path") {
    std::vector<std::string> components;
    if (!splitPath(request.stringOr("path", ""), &components, &error)) {
      response = skyplugin::errorReply("INVALID_PATH", error);
      return;
    }
    const Value* leaf = resolveRead(g_doc, components, &error);
    if (leaf == nullptr) {
      response = skyplugin::errorReply("NOT_FOUND", error);
      return;
    }
    Value data = Value::object();
    data.set("path", Value(request.stringOr("path", "")));
    data.set("value", *leaf);
    response = skyplugin::okReply(std::move(data));
    return;
  }

  if (action == "set-path") {
    const Value& pathValue = request.at("path");
    if (!pathValue.isString() || !request.contains("value")) {
      response = skyplugin::errorReply(
          "INVALID_PAYLOAD",
          "The 'set-path' action needs a string \"path\" and a \"value\".");
      return;
    }
    std::vector<std::string> components;
    if (!splitPath(pathValue.string, &components, &error)) {
      response = skyplugin::errorReply("INVALID_PATH", error);
      return;
    }
    Value* parent = resolveParent(&g_doc, components, &error);
    if (parent == nullptr) {
      response = skyplugin::errorReply("NOT_FOUND", error);
      return;
    }
    if (!writeLeaf(parent, components.back(), request.at("value"), &error)) {
      response = skyplugin::errorReply("INVALID_PATH", error);
      return;
    }
    Value data = Value::object();
    data.set("path", pathValue);
    data.set("value", request.at("value"));
    response = skyplugin::okReply(std::move(data));
    return;
  }

  if (action == "patch") {
    const Value& fields = request.at("fields");
    if (!fields.isObject()) {
      response = skyplugin::errorReply(
          "INVALID_PAYLOAD",
          "The 'patch' action needs a \"fields\" object of dot path to value.");
      return;
    }
    // Phase 1: validate every path before touching the document. All-or-nothing.
    for (const auto& change : fields.members) {
      std::vector<std::string> components;
      if (!splitPath(change.first, &components, &error) ||
          resolveParent(&g_doc, components, &error) == nullptr) {
        response = skyplugin::errorReply("INVALID_PATH", error);
        return;
      }
    }
    // Phase 2: apply every change through the same leaf rules as set-path.
    for (const auto& change : fields.members) {
      std::vector<std::string> components;
      if (!splitPath(change.first, &components, &error)) {
        response = skyplugin::errorReply("INVALID_PATH", error);
        return;
      }
      Value* parent = resolveParent(&g_doc, components, &error);
      if (parent == nullptr ||
          !writeLeaf(parent, components.back(), change.second, &error)) {
        response = skyplugin::errorReply("INVALID_PATH", error);
        return;
      }
    }
    response = skyplugin::okReply(g_doc);
    return;
  }

  if (action == "schedule-add") {
    const std::string when = request.stringOr("when", "");
    const std::string what = request.stringOr("what", "");
    Value* schedule = scheduleOf(&g_doc);
    if (schedule == nullptr) {
      response = skyplugin::errorReply(
          "INVALID_STATE", "The document has no schedule array (bug).");
      return;
    }
    if (when.empty() || what.empty() ||
        when.size() > kMaxTextLength || what.size() > kMaxTextLength) {
      response = skyplugin::errorReply(
          "INVALID_FIELD",
          "Both \"when\" and \"what\" must be 1 to " +
              std::to_string(kMaxTextLength) + " bytes.");
      return;
    }
    if (schedule->items.size() >= kMaxScheduleItems) {
      response = skyplugin::errorReply(
          "LIST_FULL",
          "The schedule already holds " + std::to_string(kMaxScheduleItems) +
              " items; remove one first.");
      return;
    }
    schedule->push(scheduleItem(when, what));
    response = skyplugin::okReply(*schedule);  // deep copy of the new list
    return;
  }

  if (action == "schedule-remove") {
    Value* schedule = scheduleOf(&g_doc);
    if (schedule == nullptr) {
      response = skyplugin::errorReply(
          "INVALID_STATE", "The document has no schedule array (bug).");
      return;
    }
    const Value& indexValue = request.at("index");
    if (!indexValue.isNumber() ||
        indexValue.number != std::floor(indexValue.number)) {
      response = skyplugin::errorReply(
          "INVALID_TYPE", "The \"index\" parameter must be a whole number.");
      return;
    }
    if (indexValue.number < 0 ||
        indexValue.number >= static_cast<double>(schedule->items.size())) {
      response = skyplugin::errorReply(
          "NOT_FOUND",
          "Index " + std::to_string(static_cast<long long>(indexValue.number)) +
              " is outside the schedule of " +
              std::to_string(schedule->items.size()) + " items.");
      return;
    }
    schedule->items.erase(schedule->items.begin() +
                          static_cast<long>(indexValue.number));
    response = skyplugin::okReply(*schedule);
    return;
  }

  // Leaving the response null tells the kit to answer UNKNOWN_ACTION.
}

// --- background event thread ---------------------------------------------------

// Pushes one tick event every kTickSeconds. Runs alongside UI calls, never
// touches the document, and relies on post_event's thread safety.
void tickLoop() {
  int uptime = 0;
  while (g_tickerRunning.load()) {
    for (int second = 0; second < kTickSeconds && g_tickerRunning.load(); ++second) {
      std::this_thread::sleep_for(std::chrono::seconds(1));
    }
    if (!g_tickerRunning.load()) break;
    uptime += kTickSeconds;
    Value event = Value::object();
    event.set("type", Value("tick"));
    event.set("seq", Value(g_tickSeq.fetch_add(1) + 1));
    event.set("uptime", Value(uptime));
    if (!skyplugin::postEvent(event)) {
      // Queue full or host shutting down: keep looping, drop this tick.
      skyplugin::log("tick event was dropped; the page may be busy");
    }
  }
}

}  // namespace

SKYPLUGIN_EXPORT int SKYBOOTSTRAP_CALL SkyPluginInit(
    const SkyBootstrapApi* api) {
  if (!skyplugin::init(api, "Struct State sample")) return 0;
  {
    std::lock_guard<std::mutex> lock(g_mutex);
    g_doc = defaults();
  }
  skyplugin::setHandler(&handleRequest);

  Value event = Value::object();
  event.set("type", Value("ready"));
  skyplugin::postEvent(event);

  g_tickerRunning = true;
  g_ticker = std::thread(tickLoop);
  return 1;
}

SKYPLUGIN_EXPORT void SKYBOOTSTRAP_CALL SkyPluginShutdown() {
  g_tickerRunning = false;
  if (g_ticker.joinable()) g_ticker.join();
  skyplugin::shutdown();
}

SKYPLUGIN_EXPORT int32_t SKYBOOTSTRAP_CALL SkyPluginUiMessage(
    const char* request_json, char* response_json, uint32_t response_capacity) {
  return skyplugin::dispatch(request_json, response_json, response_capacity);
}
