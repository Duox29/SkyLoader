// field-settings.cpp -- SkyLoader sample plugin 01: the field pattern.
//
// Data shape: one flat document of scalar fields (strings, numbers, booleans).
// The page sends lowercase-action requests; the handler validates every field
// against a rule table and either reads the document or applies a batch of
// changes atomically:
//
//   request  {"v":1,"action":"get"}
//            {"v":1,"action":"set","fields":{"volume":80,"theme":"dark"}}
//            {"v":1,"action":"reset"}
//   reply    {"v":1,"ok":true,"data":{<the full field document>}}
//            {"v":1,"ok":false,"error":{"code":"...","message":"..."}}
//
// State is in-memory only: a plugin restarts with defaults every session.
// Guides: plugins/docs/04-data-patterns.md, plugins/docs/02-writing-the-plugin-dll.md

#include "skyplugin.h"

#include <cmath>
#include <mutex>
#include <string>
#include <vector>

namespace {

using skyplugin::Value;

std::mutex g_mutex;                  // guards g_fields
Value g_fields = Value::object();    // assigned in SkyPluginInit

const char* const kThemes[] = {"auto", "dark", "light"};

// Per-field validation: type is checked outside, rules only check content.
struct FieldRule {
  const char* name;
  const char* kind;  // "string", "number", or "boolean"
  bool (*validate)(const Value& value, std::string* reason);
};

bool nicknameValid(const Value& value, std::string* reason) {
  if (value.string.empty()) {
    *reason = "must not be empty";
    return false;
  }
  if (value.string.size() > 48) {
    *reason = "must be at most 48 bytes";
    return false;
  }
  return true;
}

bool themeValid(const Value& value, std::string* reason) {
  for (const char* theme : kThemes) {
    if (value.string == theme) return true;
  }
  *reason = "must be one of: auto, dark, light";
  return false;
}

bool volumeValid(const Value& value, std::string* reason) {
  if (value.number != std::floor(value.number)) {
    *reason = "must be a whole number";
    return false;
  }
  if (value.number < 0 || value.number > 100) {
    *reason = "must be between 0 and 100";
    return false;
  }
  return true;
}

bool sensitivityValid(const Value& value, std::string* reason) {
  if (value.number < 0.25 || value.number > 8.0) {
    *reason = "must be between 0.25 and 8.0";
    return false;
  }
  return true;
}

bool notificationsValid(const Value&, std::string*) {
  return true;  // any boolean is fine
}

const FieldRule kFieldRules[] = {
    {"nickname", "string", nicknameValid},
    {"theme", "string", themeValid},
    {"volume", "number", volumeValid},
    {"sensitivity", "number", sensitivityValid},
    {"notifications", "boolean", notificationsValid},
};

const FieldRule* findRule(const std::string& name) {
  for (const FieldRule& rule : kFieldRules) {
    if (name == rule.name) return &rule;
  }
  return nullptr;
}

bool typeMatches(const FieldRule& rule, const Value& value) {
  if (std::string(rule.kind) == "string") return value.isString();
  if (std::string(rule.kind) == "number") return value.isNumber();
  return value.isBoolean();
}

const char* typeExpected(const FieldRule& rule) {
  if (std::string(rule.kind) == "string") return "a string";
  if (std::string(rule.kind) == "number") return "a number";
  return "a boolean";
}

Value defaults() {
  Value state = Value::object();
  state.set("nickname", Value("Sky Runner"));
  state.set("theme", Value("dark"));
  state.set("volume", Value(45));
  state.set("sensitivity", Value(1.0));
  state.set("notifications", Value(true));
  return state;
}

void handleRequest(const Value& request, Value& response) {
  const std::string action = request.stringOr("action", "");
  std::lock_guard<std::mutex> lock(g_mutex);

  if (action == "get") {
    // Value copies are deep: the reply is a snapshot, never a live reference.
    response = skyplugin::okReply(g_fields);
    return;
  }

  if (action == "set") {
    const Value& fields = request.at("fields");
    if (!fields.isObject()) {
      response = skyplugin::errorReply(
          "INVALID_PAYLOAD", "The 'set' action needs a \"fields\" object.");
      return;
    }

    // Phase 1: validate every change against the rule table before mutating
    // anything. A set either applies completely or not at all.
    std::string failedField;
    std::string code = "INVALID_FIELD";
    std::string message;
    for (const auto& change : fields.members) {
      const FieldRule* rule = findRule(change.first);
      if (rule == nullptr) {
        failedField = change.first;
        code = "UNKNOWN_FIELD";
        message = "There is no field named '" + change.first + "'.";
        break;
      }
      if (!typeMatches(*rule, change.second)) {
        failedField = change.first;
        code = "INVALID_TYPE";
        message = "Field '" + change.first + "' expects " +
                  typeExpected(*rule) + ".";
        break;
      }
      std::string reason;
      if (!rule->validate(change.second, &reason)) {
        failedField = change.first;
        code = "OUT_OF_RANGE";
        message = "Field '" + change.first + "' " + reason + ".";
        break;
      }
    }
    if (!message.empty()) {
      Value detail = Value::object();
      detail.set("field", Value(failedField));
      response = skyplugin::errorReply(code, message, std::move(detail));
      return;
    }

    // Phase 2: apply and repush the ready document. The reply always carries
    // the full state so the page can reconcile without guessing.
    for (const auto& change : fields.members) {
      g_fields.set(change.first, change.second);
    }

    Value changed = Value::array();
    for (const auto& change : fields.members) {
      changed.push(Value(change.first));
    }
    if (changed.size() > 0) {
      Value event = Value::object();
      event.set("type", Value("fields"));
      event.set("keys", std::move(changed));
      skyplugin::postEvent(event);  // best effort; listeners may ignore it
    }
    response = skyplugin::okReply(g_fields);
    return;
  }

  if (action == "reset") {
    g_fields = defaults();
    response = skyplugin::okReply(g_fields);
    return;
  }

  // Leaving the response null tells the kit to answer UNKNOWN_ACTION.
}

}  // namespace

SKYPLUGIN_EXPORT int SKYBOOTSTRAP_CALL SkyPluginInit(
    const SkyBootstrapApi* api) {
  if (!skyplugin::init(api, "Field Settings sample")) return 0;
  {
    std::lock_guard<std::mutex> lock(g_mutex);
    g_fields = defaults();
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
