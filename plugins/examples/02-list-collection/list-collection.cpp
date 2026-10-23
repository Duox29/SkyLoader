// list-collection.cpp -- SkyLoader sample plugin 02: the list pattern.
//
// Data shape: one authoritative array of strings plus derived stats. Mutations
// always reply with the whole array so the page can re-render without diffing:
//
//   request  {"v":1,"action":"get"}
//            {"v":1,"action":"add","item":"herald rune"}
//            {"v":1,"action":"replace","index":2,"item":"glimmer"}
//            {"v":1,"action":"remove","index":0}
//            {"v":1,"action":"move","from":2,"to":0}
//            {"v":1,"action":"sort","order":"asc"}
//            {"v":1,"action":"clear"}
//   reply    {"v":1,"ok":true,"data":{"items":[...],
//             "stats":{"count":3,"totalLength":24}}}
//            {"v":1,"ok":false,"error":{"code":"NOT_FOUND","message":"..."}}
//
// Indexes are positional: removals shift the array, so this sample never hands
// out stable item identities. Stable keys inside a struct: see sample 03's
// schedule, plus plugins/docs/04-data-patterns.md.

#include "skyplugin.h"

#include <algorithm>
#include <functional>
#include <mutex>
#include <string>
#include <vector>

namespace {

using skyplugin::Value;

std::mutex g_mutex;              // guards g_items
std::vector<std::string> g_items;

constexpr size_t kMaxItems = 200;
constexpr size_t kMaxItemLength = 140;

const char* const kSortOrders[] = {"asc", "desc", "reverse"};

bool sortOrderValid(const Value& order, std::string* reason) {
  for (const char* candidate : kSortOrders) {
    if (order.string == candidate) return true;
  }
  *reason = "must be one of: asc, desc, reverse";
  return false;
}

bool itemValid(const Value& item, std::string* reason) {
  if (item.string.empty()) {
    *reason = "must not be empty";
    return false;
  }
  if (item.string.size() > kMaxItemLength) {
    *reason = "must be at most " + std::to_string(kMaxItemLength) +
              " bytes";
    return false;
  }
  return true;
}

bool itemsFit() {
  return g_items.size() < kMaxItems;
}

Value itemsValue() {
  Value list = Value::array();
  for (const std::string& item : g_items) {
    list.push(Value(item));
  }
  return list;
}

Value statsValue() {
  size_t totalLength = 0;
  for (const std::string& item : g_items) {
    totalLength += item.size();
  }
  Value stats = Value::object();
  stats.set("count", Value(static_cast<long long>(g_items.size())));
  stats.set("totalLength", Value(static_cast<long long>(totalLength)));
  return stats;
}

// The canonical full-state reply every mutation returns.
Value listData() {
  Value data = Value::object();
  data.set("items", itemsValue());
  data.set("stats", statsValue());
  return data;
}

// Resolve request.at("index") against the current array, reporting NOT_FOUND
// with a friendly message when it falls outside.
template <typename Reply>
bool indexOfField(const Value& request, const char* key, size_t* index,
                  Reply&& reportError) {
  const Value& value = request.at(key);
  if (!value.isNumber()) {
    reportError("INVALID_TYPE",
                "The '" + std::string(key) + "' parameter must be a number.");
    return false;
  }
  const double raw = value.number;
  if (raw != std::floor(raw) || raw < 0 ||
      raw > static_cast<double>(g_items.size())) {
    reportError("NOT_FOUND",
                "Index " + std::to_string(static_cast<long long>(raw)) +
                    " is outside the list of " +
                    std::to_string(g_items.size()) + " items.");
    return false;
  }
  *index = static_cast<size_t>(raw);
  return true;
}

bool parseItem(const Value& request, std::string* item, Value* response) {
  const Value& value = request.at("item");
  if (!value.isString()) {
    *response = skyplugin::errorReply(
        "INVALID_TYPE", "The \"item\" parameter must be a string.");
    return false;
  }
  std::string reason;
  if (!itemValid(value, &reason)) {
    *response = skyplugin::errorReply(
        "OUT_OF_RANGE", "The item " + reason + ".",
        Value(static_cast<long long>(value.string.size())));
    return false;
  }
  *item = value.string;
  return true;
}
void handleRequest(const Value& request, Value& response) {
  const std::string action = request.stringOr("action", "");
  std::lock_guard<std::mutex> lock(g_mutex);

  auto reportError = [&response](std::string code, std::string message) {
    response = skyplugin::errorReply(std::move(code), std::move(message));
  };

  if (action == "get" || action == "list") {
    response = skyplugin::okReply(listData());
    return;
  }

  if (action == "add") {
    std::string item;
    if (!parseItem(request, &item, &response)) return;
    if (!itemsFit()) {
      reportError("LIST_FULL",
                  "The list already holds " +
                      std::to_string(kMaxItems) + " items; remove one first.");
      return;
    }
    g_items.push_back(std::move(item));
    response = skyplugin::okReply(listData());
    return;
  }

  if (action == "replace") {
    std::string item;
    size_t index = 0;
    if (!parseItem(request, &item, &response)) return;
    if (!indexOfField(request, "index", &index, reportError)) {
      return;
    }
    g_items[index] = std::move(item);
    response = skyplugin::okReply(listData());
    return;
  }

  if (action == "remove") {
    size_t index = 0;
    if (!indexOfField(request, "index", &index, reportError)) {
      return;
    }
    g_items.erase(g_items.begin() + static_cast<long>(index));
    response = skyplugin::okReply(listData());
    return;
  }

  if (action == "move") {
    size_t from = 0;
    size_t to = 0;
    if (!indexOfField(request, "from", &from, reportError)) return;
    if (!indexOfField(request, "to", &to, reportError)) return;
    std::string item = std::move(g_items[from]);
    g_items.erase(g_items.begin() + static_cast<long>(from));
    g_items.insert(g_items.begin() + static_cast<long>(to), std::move(item));
    response = skyplugin::okReply(listData());
    return;
  }

  if (action == "sort") {
    const Value& order = request.at("order");
    if (!order.isString() || !sortOrderValid(order, nullptr)) {
      reportError("OUT_OF_RANGE", "The \"order\" parameter must be one of: "
                                  "asc, desc, reverse.");
      return;
    }
    if (order.string == "asc") {
      std::sort(g_items.begin(), g_items.end());
    } else if (order.string == "desc") {
      std::sort(g_items.begin(), g_items.end(), std::greater<>());
    } else {
      std::reverse(g_items.begin(), g_items.end());
    }
    response = skyplugin::okReply(listData());
    return;
  }

  if (action == "clear") {
    g_items.clear();
    response = skyplugin::okReply(listData());
    return;
  }

  // Plug-ins may greet the page with a small read-only action, handy during
  // development. Anything else falls through to the UNKNOWN_ACTION envelope.
  if (action == "about") {
    Value data = Value::object();
    data.set("limits", [&] {
      Value limits = Value::object();
      limits.set("maxItems", Value(static_cast<long long>(kMaxItems)));
      limits.set("maxItemLength",
                 Value(static_cast<long long>(kMaxItemLength)));
      return limits;
    }());
    data.set("itemCount", Value(static_cast<long long>(g_items.size())));
    response = skyplugin::okReply(std::move(data));
    return;
  }
}

}  // namespace

SKYPLUGIN_EXPORT int SKYBOOTSTRAP_CALL SkyPluginInit(
    const SkyBootstrapApi* api) {
  if (!skyplugin::init(api, "List Collection sample")) return 0;
  {
    std::lock_guard<std::mutex> lock(g_mutex);
    g_items.clear();
  }
  skyplugin::setHandler(&handleRequest);
  return 1;
}

SKYPLUGIN_EXPORT void SKYBOOTSTRAP_CALL SkyPluginShutdown() {
  skyplugin::shutdown();
}

SKYPLUGIN_EXPORT int32_t SKYBOOTSTRAP_CALL SkyPluginUiMessage(
    const char* request_json, char* response_json, uint32_t response_capacity) {
  return skyplugin::dispatch(request_json, response_json, response_capacity);
}
