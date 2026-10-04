
// Read PE VERSIONINFO directly from disk. Importing an arbitrary DLL must not
// LoadLibrary it merely to discover an identity.
bool readPluginMetadata(const std::wstring& path, PluginMetadata& metadata) {
  metadata = {};
  DWORD ignored = 0;
  const DWORD size = GetFileVersionInfoSizeW(path.c_str(), &ignored);
  if (!size) return false;
  std::vector<BYTE> data(size);
  if (!GetFileVersionInfoW(path.c_str(), 0, size, data.data())) return false;

  struct Translation { WORD language; WORD codePage; };
  Translation* translations = nullptr;
  UINT translationBytes = 0;
  if (!VerQueryValueW(data.data(), L"\\VarFileInfo\\Translation",
                      reinterpret_cast<LPVOID*>(&translations), &translationBytes) ||
      !translations || translationBytes < sizeof(Translation))
    return false;

  const size_t translationCount = translationBytes / sizeof(Translation);
  for (size_t index = 0; index != translationCount; ++index) {
    const std::wstring id = canonicalPluginId(
      versionString(data, translations[index].language, translations[index].codePage,
                    L"SkyPluginId"));
    if (id.empty()) continue;
    std::wstring version = versionStringWithFallback(
      data, translations[index].language, translations[index].codePage,
      L"Version", L"ProductVersion");
    if (version.empty())
      version = versionString(data, translations[index].language,
                              translations[index].codePage, L"FileVersion");
    if (version.empty()) continue;
    metadata = {
      id,
      version,
      versionStringWithFallback(data, translations[index].language,
                                translations[index].codePage,
                                L"PluginName", L"ProductName"),
      versionStringWithFallback(data, translations[index].language,
                                translations[index].codePage,
                                L"Author", L"CompanyName"),
      versionStringWithFallback(data, translations[index].language,
                                translations[index].codePage,
                                L"Description", L"FileDescription"),
    };
    return true;
  }
  return false;
}

std::wstring pluginIdentity(const std::wstring& path) {
  PluginMetadata metadata;
  if (readPluginMetadata(path, metadata)) return L"metadata:" + metadata.id;
  std::wstring filename = fileNameOnly(path);
  const size_t dot = filename.find_last_of(L'.');
  if (dot == std::wstring::npos) return filename;
  std::wstring stem = filename.substr(0, dot);
  const size_t versionDot = stem.find_last_of(L'.');
  if (versionDot != std::wstring::npos && versionDot + 1 < stem.size()) {
    bool numericSuffix = true;
    for (size_t i = versionDot + 1; i < stem.size(); ++i) {
      if (stem[i] < L'0' || stem[i] > L'9') { numericSuffix = false; break; }
    }
    if (numericSuffix) stem.resize(versionDot);
  }
  return stem + filename.substr(dot);
}

bool samePath(const std::wstring& first, const std::wstring& second) {
  return CompareStringOrdinal(first.c_str(), -1, second.c_str(), -1, TRUE) == CSTR_EQUAL;
}

std::wstring managedPluginDirectory() {
  wchar_t localAppData[MAX_PATH]{};
  if (SHGetFolderPathW(nullptr, CSIDL_LOCAL_APPDATA, nullptr, SHGFP_TYPE_CURRENT, localAppData) != S_OK)
    return L"";
  const std::wstring root = std::wstring(localAppData) + L"\\SkyLoader";
  const std::wstring directory = root + L"\\plugins";
  CreateDirectoryW(root.c_str(), nullptr);
  CreateDirectoryW(directory.c_str(), nullptr);
  return GetFileAttributesW(directory.c_str()) != INVALID_FILE_ATTRIBUTES ? directory : L"";
}

bool importPlugin(const std::wstring& source, std::wstring& destination, std::wstring& error) {
  const std::wstring directory = managedPluginDirectory();
  if (directory.empty()) {
    error = L"Could not create the managed plugin directory.";
    return false;
  }
  const std::wstring filename = fileNameOnly(source);
  destination = directory + L"\\" + filename;
  if (samePath(source, destination)) return true;
  if (!CopyFileW(source.c_str(), destination.c_str(), FALSE)) {
    error = L"Could not copy the DLL into the managed plugin directory (" +
            std::to_wstring(GetLastError()) + L").";
    destination.clear();
    return false;
  }
  return true;
}

bool isManagedPluginPath(const std::wstring& path) {
  const std::wstring directory = managedPluginDirectory();
  if (directory.empty() || path.size() <= directory.size()) return false;
  if (CompareStringOrdinal(path.c_str(), static_cast<int>(directory.size()),
                           directory.c_str(), static_cast<int>(directory.size()), TRUE) != CSTR_EQUAL)
    return false;
  return path[directory.size()] == L'\\' || path[directory.size()] == L'/';
}

enum class PluginImportAction { Failed, Unchanged, Added, Updated };

PluginImportAction importAndRegisterPlugin(HWND owner, const std::wstring& source,
                                           std::wstring& message) {
  PluginMetadata incomingMetadata;
  const bool hasIncomingMetadata = readPluginMetadata(source, incomingMetadata);
  const std::wstring identity = pluginIdentity(source);
  bool replacing = false;
  bool metadataReplacement = false;

  for (const auto& registered : gPlugins) {
    if (CompareStringOrdinal(pluginIdentity(registered.path).c_str(), -1,
                             identity.c_str(), -1, TRUE) != CSTR_EQUAL)
      continue;
    replacing = true;
    PluginMetadata installedMetadata;
    if (hasIncomingMetadata && readPluginMetadata(registered.path, installedMetadata) &&
        installedMetadata.id == incomingMetadata.id) {
      metadataReplacement = true;
      if (compareVersions(incomingMetadata.version, installedMetadata.version) < 0) {
        message = L"A newer " + incomingMetadata.id + L" version (" +
                  installedMetadata.version + L") is already installed.";
        return PluginImportAction::Unchanged;
      }
    }
  }

  if (replacing && !metadataReplacement &&
      MessageBoxW(owner, L"A plugin with this name is already imported. Replace it?",
                  L"Update Plugin", MB_YESNO | MB_ICONQUESTION) != IDYES) {
    message = L"Plugin import cancelled.";
    return PluginImportAction::Unchanged;
  }

  std::wstring importedPath;
  std::wstring importError;
  if (!importPlugin(source, importedPath, importError)) {
    message = importError;
    return PluginImportAction::Failed;
  }

  std::vector<PluginItem> updatedPaths;
  bool replacedEntry = false;
  bool staleFileLeft = false;
  for (const auto& registered : gPlugins) {
    const bool samePlugin = CompareStringOrdinal(pluginIdentity(registered.path).c_str(), -1,
                                                   identity.c_str(), -1, TRUE) == CSTR_EQUAL;
    if (!samePlugin) {
      updatedPaths.push_back(registered);
      continue;
    }
    if (!replacedEntry) {
      updatedPaths.push_back({importedPath, registered.enabled});
      replacedEntry = true;
    }
    if (isManagedPluginPath(registered.path) && !samePath(registered.path, importedPath) &&
        !DeleteFileW(registered.path.c_str()))
      staleFileLeft = true;
  }
  if (!replacedEntry) updatedPaths.push_back({importedPath, true});
  gPlugins = std::move(updatedPaths);

  if (!replacing) {
    message = L"Plugin imported into managed directory.";
    return PluginImportAction::Added;
  }
  message = metadataReplacement
    ? L"Plugin updated from manifest: " + incomingMetadata.id + L" " + incomingMetadata.version + L"."
    : L"Plugin updated.";
  if (staleFileLeft) message += L" The old managed DLL could not be deleted.";
  return PluginImportAction::Updated;
}
