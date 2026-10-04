
void setStatus(const std::wstring& text) {
  if (gStatus) {
    SetWindowTextW(gStatus, text.c_str());
  }
}

std::wstring utf8ToWide(const std::string& value) {
  if (value.empty()) return L"";
  const int needed = MultiByteToWideChar(CP_UTF8, 0, value.c_str(), static_cast<int>(value.size()),
                                         nullptr, 0);
  if (needed <= 0) return L"";
  std::wstring wide(static_cast<size_t>(needed), L'\0');
  MultiByteToWideChar(CP_UTF8, 0, value.c_str(), static_cast<int>(value.size()),
                      &wide[0], needed);
  return wide;
}

std::string findJsonString(const std::string& json, const char* key) {
  const std::string marker = std::string("\"") + key + "\"";
  size_t pos = json.find(marker);
  if (pos == std::string::npos) return "";
  pos = json.find(':', pos + marker.size());
  if (pos == std::string::npos) return "";
  pos = json.find('"', pos + 1);
  if (pos == std::string::npos) return "";
  std::string result;
  bool escaped = false;
  for (++pos; pos < json.size(); ++pos) {
    const char ch = json[pos];
    if (escaped) {
      result.push_back(ch);
      escaped = false;
      continue;
    }
    if (ch == '\\') {
      escaped = true;
      continue;
    }
    if (ch == '"') break;
    result.push_back(ch);
  }
  return result;
}

std::string findReleaseExeAssetUrl(const std::string& json) {
  const std::string marker = "\"browser_download_url\"";
  size_t pos = 0;
  std::string fallback;
  while ((pos = json.find(marker, pos)) != std::string::npos) {
    pos = json.find(':', pos + marker.size());
    if (pos == std::string::npos) break;
    pos = json.find('"', pos + 1);
    if (pos == std::string::npos) break;

    std::string value;
    bool escaped = false;
    for (++pos; pos < json.size(); ++pos) {
      const char ch = json[pos];
      if (escaped) {
        value.push_back(ch);
        escaped = false;
        continue;
      }
      if (ch == '\\') {
        escaped = true;
        continue;
      }
      if (ch == '"') break;
      value.push_back(ch);
    }

    std::string lower = value;
    std::transform(lower.begin(), lower.end(), lower.begin(),
                   [](unsigned char c) { return static_cast<char>(std::tolower(c)); });
    if (lower.size() >= 4 && lower.substr(lower.size() - 4) == ".exe") {
      if (lower.find("skyloader") != std::string::npos ||
          lower.find("sky-loader") != std::string::npos ||
          lower.find("sky loader") != std::string::npos) {
        return value;
      }
      if (fallback.empty()) fallback = value;
    }
  }
  return fallback;
}

std::wstring updatesDirectory() {
  wchar_t localAppData[MAX_PATH]{};
  if (SHGetFolderPathW(nullptr, CSIDL_LOCAL_APPDATA, nullptr, SHGFP_TYPE_CURRENT, localAppData) != S_OK)
    return L"";
  const std::wstring root = std::wstring(localAppData) + L"\\SkyLoader";
  const std::wstring directory = root + L"\\updates";
  CreateDirectoryW(root.c_str(), nullptr);
  CreateDirectoryW(directory.c_str(), nullptr);
  return GetFileAttributesW(directory.c_str()) != INVALID_FILE_ATTRIBUTES ? directory : L"";
}

std::wstring safeVersionForFile(std::wstring version) {
  while (!version.empty() && iswspace(version.front())) version.erase(version.begin());
  while (!version.empty() && iswspace(version.back())) version.pop_back();
  if (!version.empty() && (version.front() == L'v' || version.front() == L'V'))
    version.erase(version.begin());
  if (version.empty()) version = L"latest";
  for (wchar_t& ch : version) {
    const bool valid = (ch >= L'0' && ch <= L'9') ||
                       (ch >= L'a' && ch <= L'z') ||
                       (ch >= L'A' && ch <= L'Z') ||
                       ch == L'.' || ch == L'-' || ch == L'_';
    if (!valid) ch = L'_';
  }
  return version;
}

bool downloadUrlToFile(const std::wstring& url, const std::wstring& destination,
                       std::wstring& error) {
  URL_COMPONENTSW components{};
  components.dwStructSize = sizeof(components);
  wchar_t host[256]{};
  wchar_t path[4096]{};
  wchar_t extra[2048]{};
  components.lpszHostName = host;
  components.dwHostNameLength = ARRAYSIZE(host);
  components.lpszUrlPath = path;
  components.dwUrlPathLength = ARRAYSIZE(path);
  components.lpszExtraInfo = extra;
  components.dwExtraInfoLength = ARRAYSIZE(extra);

  if (!WinHttpCrackUrl(url.c_str(), static_cast<DWORD>(url.size()), 0, &components) ||
      components.nScheme != INTERNET_SCHEME_HTTPS) {
    error = L"Invalid update download URL.";
    return false;
  }

  const std::wstring userAgent = std::wstring(L"SkyLoader/") + kAppVersion;
  HINTERNET session = WinHttpOpen(userAgent.c_str(),
                                  WINHTTP_ACCESS_TYPE_DEFAULT_PROXY,
                                  WINHTTP_NO_PROXY_NAME,
                                  WINHTTP_NO_PROXY_BYPASS, 0);
  if (!session) {
    error = L"Could not start WinHTTP.";
    return false;
  }

  HINTERNET connection = WinHttpConnect(session, std::wstring(host, components.dwHostNameLength).c_str(),
                                        components.nPort, 0);
  if (!connection) {
    WinHttpCloseHandle(session);
    error = L"Could not connect to update host.";
    return false;
  }

  const std::wstring objectPath =
    std::wstring(path, components.dwUrlPathLength) + std::wstring(extra, components.dwExtraInfoLength);
  HINTERNET request = WinHttpOpenRequest(
      connection, L"GET", objectPath.c_str(), nullptr, WINHTTP_NO_REFERER,
      WINHTTP_DEFAULT_ACCEPT_TYPES, WINHTTP_FLAG_SECURE);
  if (!request) {
    WinHttpCloseHandle(connection);
    WinHttpCloseHandle(session);
    error = L"Could not create update download request.";
    return false;
  }

  DWORD redirectPolicy = WINHTTP_OPTION_REDIRECT_POLICY_ALWAYS;
  WinHttpSetOption(request, WINHTTP_OPTION_REDIRECT_POLICY, &redirectPolicy, sizeof(redirectPolicy));

  const wchar_t headers[] = L"Accept: application/octet-stream\r\n";
  const bool ok = WinHttpSendRequest(request, headers, static_cast<DWORD>(-1),
                                     WINHTTP_NO_REQUEST_DATA, 0, 0, 0) &&
                  WinHttpReceiveResponse(request, nullptr);

  DWORD status = 0;
  DWORD statusSize = sizeof(status);
  WinHttpQueryHeaders(request,
                      WINHTTP_QUERY_STATUS_CODE | WINHTTP_QUERY_FLAG_NUMBER,
                      WINHTTP_HEADER_NAME_BY_INDEX, &status, &statusSize,
                      WINHTTP_NO_HEADER_INDEX);

  if (!ok || status < 200 || status >= 300) {
    WinHttpCloseHandle(request);
    WinHttpCloseHandle(connection);
    WinHttpCloseHandle(session);
    error = L"Update download failed.";
    return false;
  }

  HANDLE file = CreateFileW(destination.c_str(), GENERIC_WRITE, 0, nullptr, CREATE_ALWAYS,
                            FILE_ATTRIBUTE_NORMAL, nullptr);
  if (file == INVALID_HANDLE_VALUE) {
    WinHttpCloseHandle(request);
    WinHttpCloseHandle(connection);
    WinHttpCloseHandle(session);
    error = L"Could not create downloaded update file.";
    return false;
  }

  bool writeOk = true;
  for (;;) {
    DWORD available = 0;
    if (!WinHttpQueryDataAvailable(request, &available)) {
      writeOk = false;
      break;
    }
    if (available == 0) break;
    std::vector<char> chunk(available);
    DWORD read = 0;
    if (!WinHttpReadData(request, chunk.data(), available, &read)) {
      writeOk = false;
      break;
    }
    if (read == 0) break;
    DWORD written = 0;
    if (!WriteFile(file, chunk.data(), read, &written, nullptr) || written != read) {
      writeOk = false;
      break;
    }
  }

  CloseHandle(file);
  WinHttpCloseHandle(request);
  WinHttpCloseHandle(connection);
  WinHttpCloseHandle(session);

  if (!writeOk) {
    DeleteFileW(destination.c_str());
    error = L"Could not write the downloaded update.";
    return false;
  }
  return true;
}

std::wstring normalizedVersion(std::wstring version) {
  while (!version.empty() && iswspace(version.front())) version.erase(version.begin());
  while (!version.empty() && iswspace(version.back())) version.pop_back();
  if (!version.empty() && (version.front() == L'v' || version.front() == L'V'))
    version.erase(version.begin());
  return version;
}

int nextVersionNumber(const std::wstring& version, size_t& pos) {
  while (pos < version.size() && !iswdigit(version[pos])) ++pos;
  int value = 0;
  while (pos < version.size() && iswdigit(version[pos])) {
    value = value * 10 + static_cast<int>(version[pos] - L'0');
    ++pos;
  }
  return value;
}

int compareVersions(std::wstring first, std::wstring second) {
  first = normalizedVersion(first);
  second = normalizedVersion(second);
  size_t a = 0;
  size_t b = 0;
  for (int i = 0; i < 4; ++i) {
    const int av = nextVersionNumber(first, a);
    const int bv = nextVersionNumber(second, b);
    if (av != bv) return av < bv ? -1 : 1;
  }
  return CompareStringOrdinal(first.c_str(), -1, second.c_str(), -1, TRUE) == CSTR_EQUAL ? 0 :
         (first < second ? -1 : 1);
}

bool fetchLatestRelease(UpdateCheckResult& result) {
  const std::wstring userAgent = std::wstring(L"SkyLoader/") + kAppVersion;
  HINTERNET session = WinHttpOpen(userAgent.c_str(),
                                  WINHTTP_ACCESS_TYPE_DEFAULT_PROXY,
                                  WINHTTP_NO_PROXY_NAME,
                                  WINHTTP_NO_PROXY_BYPASS, 0);
  if (!session) {
    result.message = L"Could not start WinHTTP.";
    return false;
  }

  HINTERNET connection = WinHttpConnect(session, L"api.github.com",
                                        INTERNET_DEFAULT_HTTPS_PORT, 0);
  if (!connection) {
    WinHttpCloseHandle(session);
    result.message = L"Could not connect to GitHub.";
    return false;
  }

  HINTERNET request = WinHttpOpenRequest(
      connection, L"GET", L"/repos/HerokeyVN/SkyLoader/releases/latest",
      nullptr, WINHTTP_NO_REFERER, WINHTTP_DEFAULT_ACCEPT_TYPES, WINHTTP_FLAG_SECURE);
  if (!request) {
    WinHttpCloseHandle(connection);
    WinHttpCloseHandle(session);
    result.message = L"Could not create GitHub request.";
    return false;
  }

  const wchar_t headers[] = L"Accept: application/vnd.github+json\r\n"
                            L"X-GitHub-Api-Version: 2022-11-28\r\n";
  bool ok = WinHttpSendRequest(request, headers, static_cast<DWORD>(-1),
                               WINHTTP_NO_REQUEST_DATA, 0, 0, 0) &&
            WinHttpReceiveResponse(request, nullptr);

  DWORD status = 0;
  DWORD statusSize = sizeof(status);
  WinHttpQueryHeaders(request,
                      WINHTTP_QUERY_STATUS_CODE | WINHTTP_QUERY_FLAG_NUMBER,
                      WINHTTP_HEADER_NAME_BY_INDEX, &status, &statusSize,
                      WINHTTP_NO_HEADER_INDEX);

  std::string body;
  if (ok) {
    for (;;) {
      DWORD available = 0;
      if (!WinHttpQueryDataAvailable(request, &available) || available == 0) break;
      std::string chunk(available, '\0');
      DWORD read = 0;
      if (!WinHttpReadData(request, &chunk[0], available, &read) || read == 0) break;
      chunk.resize(read);
      body += chunk;
    }
  }

  WinHttpCloseHandle(request);
  WinHttpCloseHandle(connection);
  WinHttpCloseHandle(session);

  if (!ok || status < 200 || status >= 300) {
    result.message = status == 404
      ? L"No GitHub release has been published yet."
      : L"GitHub update check failed.";
    return false;
  }

  result.latestVersion = utf8ToWide(findJsonString(body, "tag_name"));
  result.releaseUrl = utf8ToWide(findJsonString(body, "html_url"));
  result.assetUrl = utf8ToWide(findReleaseExeAssetUrl(body));
  if (result.latestVersion.empty()) {
    result.message = L"GitHub release response did not include a version.";
    return false;
  }
  if (result.releaseUrl.empty()) result.releaseUrl = kReleasePageUrl;
  result.updateAvailable = compareVersions(kAppVersion, result.latestVersion) < 0;
  if (result.updateAvailable) {
    if (result.assetUrl.empty()) {
      result.message = L"Update available, but the release has no .exe asset.";
      return false;
    }

    const std::wstring directory = updatesDirectory();
    if (directory.empty()) {
      result.message = L"Update available, but the update folder could not be created.";
      return false;
    }

    result.downloadedPath = directory + L"\\SkyLoader-" +
                            safeVersionForFile(result.latestVersion) + L".exe";
    std::wstring downloadError;
    if (!downloadUrlToFile(result.assetUrl, result.downloadedPath, downloadError)) {
      result.message = downloadError.empty()
        ? L"Update available, but the download failed."
        : downloadError;
      return false;
    }
  }
  result.ok = true;
  result.message = result.updateAvailable
    ? L"Update downloaded: " + result.latestVersion
    : L"SkyLoader is up to date.";
  return true;
}

void startUpdateCheck(HWND owner, bool manual) {
  if (gUpdateCheckInProgress) {
    if (manual) setStatus(L"Update check is already running...");
    return;
  }
  gUpdateCheckInProgress = true;
  if (manual) setStatus(L"Checking and downloading updates...");
  std::thread([owner, manual]() {
    auto* result = new UpdateCheckResult();
    result->manual = manual;
    fetchLatestRelease(*result);
    if (IsWindow(owner)) {
      PostMessageW(owner, WM_UPDATE_CHECK_COMPLETE, reinterpret_cast<WPARAM>(result), 0);
    } else {
      delete result;
    }
  }).detach();
}
