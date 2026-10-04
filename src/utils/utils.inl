
std::wstring moduleDirectory() {
  wchar_t path[MAX_PATH]{};
  GetModuleFileNameW(nullptr, path, MAX_PATH);
  std::wstring result(path);
  const auto separator = result.find_last_of(L"\\/");
  return separator == std::wstring::npos ? L"." : result.substr(0, separator);
}

std::wstring normalizedPath(const std::wstring& path) {
  wchar_t full[MAX_PATH]{};
  const DWORD count = GetFullPathNameW(path.c_str(), MAX_PATH, full, nullptr);
  return count && count < MAX_PATH ? std::wstring(full) : path;
}

std::wstring fileNameOnly(const std::wstring& path) {
  const size_t slash = path.find_last_of(L"\\/");
  return slash == std::wstring::npos ? path : path.substr(slash + 1);
}

int compareVersions(std::wstring first, std::wstring second);

std::wstring canonicalPluginId(std::wstring value) {
  if (value.empty() || value.size() > 128) return L"";
  for (wchar_t& character : value) {
    if (!((character >= L'a' && character <= L'z') ||
          (character >= L'A' && character <= L'Z') ||
          (character >= L'0' && character <= L'9') ||
          character == L'.' || character == L'_' || character == L'-'))
      return L"";
    character = static_cast<wchar_t>(towlower(character));
  }
  return value;
}

std::wstring versionString(const std::vector<BYTE>& data, WORD language,
                           WORD codePage, const wchar_t* key) {
  wchar_t query[128]{};
  swprintf(query, sizeof(query) / sizeof(query[0]),
           L"\\StringFileInfo\\%04x%04x\\%ls", language, codePage, key);
  LPWSTR value = nullptr;
  UINT length = 0;
  if (!VerQueryValueW(data.data(), query, reinterpret_cast<LPVOID*>(&value), &length) ||
      !value || !length)
    return L"";
  std::wstring result(value, length);
  const size_t terminator = result.find(L'\0');
  if (terminator != std::wstring::npos) result.resize(terminator);
  return result;
}

std::wstring versionStringWithFallback(const std::vector<BYTE>& data, WORD language,
                                       WORD codePage, const wchar_t* primary,
                                       const wchar_t* legacy) {
  std::wstring result = versionString(data, language, codePage, primary);
  return result.empty() ? versionString(data, language, codePage, legacy) : result;
}
