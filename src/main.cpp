#include <windows.h>
#include <commctrl.h>
#include <commdlg.h>
#include <tlhelp32.h>
#include <shellapi.h>
#include <shlobj.h>
#include <uxtheme.h>
#include <winver.h>
#include <winhttp.h>

#include <algorithm>
#include <cctype>
#include <cwctype>
#include <memory>
#include <string>
#include <thread>
#include <vector>

#include "loader_controller.h"

namespace {

constexpr UINT WM_INJECTION_COMPLETE = WM_USER + 101;
constexpr UINT WM_UPDATE_CHECK_COMPLETE = WM_USER + 102;

constexpr int kIdGamePath = 1001;
constexpr int kIdBrowseGame = 1002;
constexpr int kIdLaunchGame = 1003;
constexpr int kIdDllList = 1004;
constexpr int kIdAddDll = 1005;
constexpr int kIdOpenFolder = 1006;
constexpr int kIdRemoveDll = 1007;
constexpr int kIdStatus = 1008;
constexpr int kIdCheckUpdates = 1009;

constexpr wchar_t kAppVersion[] = L"0.1.1";
constexpr wchar_t kReleasePageUrl[] = L"https://github.com/HerokeyVN/SkyLoader/releases";
constexpr wchar_t kSkySteamUri[] = L"steam://run/2325290";
constexpr UINT_PTR kAutoInjectTimer = 1;
constexpr UINT kAutoInjectPollMs = 50;

struct PluginItem {
  std::wstring path;
  bool enabled{true};
};

struct PluginMetadata {
  std::wstring id;
  std::wstring version;
  std::wstring name;
  std::wstring author;
  std::wstring description;
};

HWND gMainWindow = nullptr;
HWND gPluginsTitle = nullptr;
HWND gAddDllBtn = nullptr;
HWND gOpenFolderBtn = nullptr;
HWND gRemoveDllBtn = nullptr;
HWND gCheckUpdatesBtn = nullptr;
HWND gDllList = nullptr;
HWND gHintText = nullptr;
HWND gGamePathLabel = nullptr;
HWND gGamePath = nullptr;
HWND gBrowseBtn = nullptr;
HWND gLaunchBtn = nullptr;
HWND gStatus = nullptr;

HFONT gFontRegular = nullptr;
HFONT gFontSemibold = nullptr;
HFONT gFontBold = nullptr;
HFONT gFontLaunchBtn = nullptr;

HBRUSH gBgBrush = nullptr;
HBRUSH gFooterBrush = nullptr;

WNDPROC gOrigLaunchBtnProc = nullptr;
bool gLaunchHover = false;
WNDPROC gOrigAddDllBtnProc = nullptr;
bool gAddDllHover = false;
bool gIgnoreItemChanged = false;
bool gUpdateCheckInProgress = false;

std::wstring gIniPath;
std::wstring gBootstrapPath;
std::vector<PluginItem> gPlugins;
bool gAutoInjectPending = false;
DWORD gPendingSkyPid = 0;
UINT gPendingSkyTicks = 0;

struct UpdateCheckResult {
  bool manual{};
  bool ok{};
  bool updateAvailable{};
  std::wstring latestVersion;
  std::wstring releaseUrl;
  std::wstring assetUrl;
  std::wstring downloadedPath;
  std::wstring message;
};
#include "utils/utils.inl"
#include "plugins/plugins.inl"
#include "update/update.inl"

void populateDllList() {
  gIgnoreItemChanged = true;
  ListView_DeleteAllItems(gDllList);
  for (size_t i = 0; i < gPlugins.size(); ++i) {
    PluginMetadata metadata;
    const bool hasMetadata = readPluginMetadata(gPlugins[i].path, metadata);
    const std::wstring displayName = hasMetadata && !metadata.name.empty()
      ? metadata.name : fileNameOnly(gPlugins[i].path);
    const std::wstring displayVersion = hasMetadata ? metadata.version : L"—";
    const std::wstring displayAuthor = hasMetadata && !metadata.author.empty()
      ? metadata.author : L"—";
    const std::wstring displayDescription = hasMetadata && !metadata.description.empty()
      ? metadata.description : L"—";
    LVITEMW item{};
    item.mask = LVIF_TEXT;
    item.iItem = static_cast<int>(i);
    item.pszText = const_cast<wchar_t*>(displayName.c_str());
    ListView_InsertItem(gDllList, &item);
    ListView_SetItemText(gDllList, static_cast<int>(i), 1,
                         const_cast<wchar_t*>(displayVersion.c_str()));
    ListView_SetItemText(gDllList, static_cast<int>(i), 2,
                         const_cast<wchar_t*>(displayAuthor.c_str()));
    ListView_SetItemText(gDllList, static_cast<int>(i), 3,
                         const_cast<wchar_t*>(displayDescription.c_str()));

    ListView_SetCheckState(gDllList, static_cast<int>(i), gPlugins[i].enabled ? TRUE : FALSE);
  }
  gIgnoreItemChanged = false;
}

void saveSettings() {
  wchar_t gamePath[MAX_PATH]{};
  GetWindowTextW(gGamePath, gamePath, MAX_PATH);
  WritePrivateProfileStringW(L"SkyLoader", L"GamePath", gamePath, gIniPath.c_str());
  WritePrivateProfileStringW(L"SkyLoader", L"BootstrapPath", gBootstrapPath.c_str(), gIniPath.c_str());
  WritePrivateProfileStringW(L"Dlls", L"Count", std::to_wstring(gPlugins.size()).c_str(), gIniPath.c_str());

  for (size_t i = 0; i < gPlugins.size(); ++i) {
    const std::wstring key = L"Dll" + std::to_wstring(i);
    const std::wstring enabledKey = L"DllEnabled" + std::to_wstring(i);
    WritePrivateProfileStringW(L"Dlls", key.c_str(), gPlugins[i].path.c_str(), gIniPath.c_str());
    WritePrivateProfileStringW(L"Dlls", enabledKey.c_str(), gPlugins[i].enabled ? L"1" : L"0", gIniPath.c_str());
  }
  for (size_t i = gPlugins.size(); i < 256; ++i) {
    const std::wstring key = L"Dll" + std::to_wstring(i);
    const std::wstring enabledKey = L"DllEnabled" + std::to_wstring(i);
    WritePrivateProfileStringW(L"Dlls", key.c_str(), nullptr, gIniPath.c_str());
    WritePrivateProfileStringW(L"Dlls", enabledKey.c_str(), nullptr, gIniPath.c_str());
  }
}

bool fileExists(const std::wstring& path) {
  if (path.empty()) return false;
  DWORD attr = GetFileAttributesW(path.c_str());
  return (attr != INVALID_FILE_ATTRIBUTES && !(attr & FILE_ATTRIBUTE_DIRECTORY));
}

std::wstring getSteamPathFromRegistry() {
  wchar_t buffer[MAX_PATH]{};
  DWORD size = sizeof(buffer);
  if (RegGetValueW(HKEY_CURRENT_USER, L"Software\\Valve\\Steam", L"SteamPath",
                   RRF_RT_REG_SZ, nullptr, buffer, &size) == ERROR_SUCCESS && buffer[0]) {
    std::wstring path(buffer);
    std::replace(path.begin(), path.end(), L'/', L'\\');
    return path;
  }
  size = sizeof(buffer);
  if (RegGetValueW(HKEY_LOCAL_MACHINE, L"SOFTWARE\\WOW6432Node\\Valve\\Steam", L"InstallPath",
                   RRF_RT_REG_SZ, nullptr, buffer, &size) == ERROR_SUCCESS && buffer[0]) {
    std::wstring path(buffer);
    std::replace(path.begin(), path.end(), L'/', L'\\');
    return path;
  }
  size = sizeof(buffer);
  if (RegGetValueW(HKEY_LOCAL_MACHINE, L"SOFTWARE\\Valve\\Steam", L"InstallPath",
                   RRF_RT_REG_SZ, nullptr, buffer, &size) == ERROR_SUCCESS && buffer[0]) {
    std::wstring path(buffer);
    std::replace(path.begin(), path.end(), L'/', L'\\');
    return path;
  }
  return L"";
}

std::vector<std::wstring> getSteamLibraryFolders(const std::wstring& steamPath) {
  std::vector<std::wstring> folders;
  if (steamPath.empty()) return folders;
  folders.push_back(steamPath);

  const std::wstring vdfPath = steamPath + L"\\steamapps\\libraryfolders.vdf";
  HANDLE file = CreateFileW(vdfPath.c_str(), GENERIC_READ, FILE_SHARE_READ, nullptr, OPEN_EXISTING, 0, nullptr);
  if (file != INVALID_HANDLE_VALUE) {
    DWORD fileSize = GetFileSize(file, nullptr);
    if (fileSize != INVALID_FILE_SIZE && fileSize > 0 && fileSize < 5 * 1024 * 1024) {
      std::string content(fileSize, '\0');
      DWORD readBytes = 0;
      if (ReadFile(file, &content[0], fileSize, &readBytes, nullptr) && readBytes > 0) {
        size_t pos = 0;
        while ((pos = content.find("\"path\"", pos)) != std::string::npos) {
          pos += 6;
          size_t startQuote = content.find('\"', pos);
          if (startQuote == std::string::npos) break;
          size_t endQuote = content.find('\"', startQuote + 1);
          if (endQuote == std::string::npos) break;

          std::string rawPath = content.substr(startQuote + 1, endQuote - startQuote - 1);
          std::string unescaped;
          for (size_t i = 0; i < rawPath.size(); ++i) {
            if (rawPath[i] == '\\' && i + 1 < rawPath.size() && rawPath[i + 1] == '\\') {
              unescaped += '\\';
              ++i;
            } else {
              unescaped += rawPath[i];
            }
          }
          if (!unescaped.empty()) {
            wchar_t wbuf[MAX_PATH]{};
            if (MultiByteToWideChar(CP_UTF8, 0, unescaped.c_str(), -1, wbuf, MAX_PATH)) {
              std::wstring folder(wbuf);
              std::replace(folder.begin(), folder.end(), L'/', L'\\');
              if (std::find_if(folders.begin(), folders.end(), [&](const std::wstring& f) {
                    return samePath(f, folder);
                  }) == folders.end()) {
                folders.push_back(folder);
              }
            }
          }
          pos = endQuote + 1;
        }
      }
    }
    CloseHandle(file);
  }
  return folders;
}

std::wstring autoDetectSkyGamePath() {
  const std::wstring relSubpath = L"\\steamapps\\common\\Sky Children of the Light\\Sky.exe";

  // 1. Check Steam installation and all configured Steam libraries
  const std::wstring steamPath = getSteamPathFromRegistry();
  if (!steamPath.empty()) {
    std::vector<std::wstring> libraries = getSteamLibraryFolders(steamPath);
    for (const auto& lib : libraries) {
      std::wstring candidate = lib + relSubpath;
      if (fileExists(candidate)) {
        return normalizedPath(candidate);
      }
    }
  }

  // 2. Scan all logical drives for standard Steam library locations
  wchar_t drives[512]{};
  if (GetLogicalDriveStringsW(512, drives)) {
    const wchar_t* d = drives;
    while (*d) {
      std::wstring driveRoot(d);
      if (!driveRoot.empty() && driveRoot.back() == L'\\') {
        driveRoot.pop_back();
      }
      const std::wstring commonPrefixes[] = {
        L"",
        L"\\SteamLibrary",
        L"\\Steam",
        L"\\Program Files (x86)\\Steam",
        L"\\Program Files\\Steam",
        L"\\Games\\SteamLibrary",
        L"\\Games\\Steam",
        L"\\Games"
      };
      for (const auto& prefix : commonPrefixes) {
        std::wstring candidate = driveRoot + prefix + relSubpath;
        if (fileExists(candidate)) {
          return normalizedPath(candidate);
        }
      }
      d += wcslen(d) + 1;
    }
  }

  return L"";
}

void loadSettings() {
  const wchar_t* defaultFallback =
    L"C:\\Program Files (x86)\\Steam\\steamapps\\common\\Sky Children of the Light\\Sky.exe";
  wchar_t savedGamePath[MAX_PATH]{};
  GetPrivateProfileStringW(L"SkyLoader", L"GamePath", L"", savedGamePath, MAX_PATH, gIniPath.c_str());

  std::wstring gamePath = savedGamePath;
  if (gamePath.empty() || !fileExists(gamePath)) {
    const std::wstring detected = autoDetectSkyGamePath();
    if (!detected.empty()) {
      gamePath = detected;
      WritePrivateProfileStringW(L"SkyLoader", L"GamePath", gamePath.c_str(), gIniPath.c_str());
      setStatus(L"Auto-detected Sky.exe in Steam library.");
    } else {
      gamePath = defaultFallback;
      if (!fileExists(gamePath)) {
        setStatus(L"Sky.exe not found at default path. Click Browse to locate Sky.exe.");
      }
    }
  }
  SetWindowTextW(gGamePath, gamePath.c_str());

  const std::wstring defaultBootstrap = moduleDirectory() + L"\\SkyBootstrap.dll";
  wchar_t bootstrapPath[MAX_PATH]{};
  GetPrivateProfileStringW(L"SkyLoader", L"BootstrapPath", defaultBootstrap.c_str(),
                           bootstrapPath, MAX_PATH, gIniPath.c_str());
  gBootstrapPath = bootstrapPath;
  const std::wstring legacyBootstrap =
    L"D:\\Develop\\Language\\C++\\SkyBootstrap\\dist\\SkyBootstrap.dll";
  if (samePath(gBootstrapPath, legacyBootstrap))
    gBootstrapPath = defaultBootstrap;

  const UINT count = GetPrivateProfileIntW(L"Dlls", L"Count", 0, gIniPath.c_str());
  gPlugins.clear();
  for (UINT i = 0; i < count && i < 256; ++i) {
    const std::wstring key = L"Dll" + std::to_wstring(i);
    const std::wstring enabledKey = L"DllEnabled" + std::to_wstring(i);
    wchar_t value[MAX_PATH]{};
    GetPrivateProfileStringW(L"Dlls", key.c_str(), L"", value, MAX_PATH, gIniPath.c_str());
    if (value[0] && GetFileAttributesW(value) != INVALID_FILE_ATTRIBUTES) {
      const UINT enabled = GetPrivateProfileIntW(L"Dlls", enabledKey.c_str(), 1, gIniPath.c_str());
      gPlugins.push_back({value, enabled != 0});
    }
  }
  populateDllList();
}

bool chooseFile(HWND owner, const wchar_t* filter, std::wstring& output) {
  wchar_t path[MAX_PATH]{};
  OPENFILENAMEW dialog{};
  dialog.lStructSize = sizeof(dialog);
  dialog.hwndOwner = owner;
  dialog.lpstrFilter = filter;
  dialog.lpstrFile = path;
  dialog.nMaxFile = MAX_PATH;
  dialog.Flags = OFN_FILEMUSTEXIST | OFN_PATHMUSTEXIST;
  if (!GetOpenFileNameW(&dialog))
    return false;
  output = normalizedPath(path);
  return true;
}

DWORD findSkyProcess(const std::wstring& expectedPath, std::wstring* actualDetectedPath = nullptr) {
  HANDLE snapshot = CreateToolhelp32Snapshot(TH32CS_SNAPPROCESS, 0);
  if (snapshot == INVALID_HANDLE_VALUE)
    return 0;

  PROCESSENTRY32W process{};
  process.dwSize = sizeof(process);
  DWORD pid = 0;
  if (Process32FirstW(snapshot, &process)) {
    do {
      if (CompareStringOrdinal(process.szExeFile, -1, L"Sky.exe", -1, TRUE) != CSTR_EQUAL)
        continue;
      HANDLE candidate = OpenProcess(PROCESS_QUERY_LIMITED_INFORMATION, FALSE, process.th32ProcessID);
      if (!candidate) continue;
      wchar_t imagePath[MAX_PATH]{};
      DWORD imageLength = MAX_PATH;
      const bool queried = QueryFullProcessImageNameW(candidate, 0, imagePath, &imageLength) != FALSE;
      CloseHandle(candidate);
      if (queried) {
        std::wstring normalizedImage = normalizedPath(imagePath);
        if (expectedPath.empty() || samePath(normalizedImage, normalizedPath(expectedPath))) {
          pid = process.th32ProcessID;
          if (actualDetectedPath) *actualDetectedPath = normalizedImage;
          break;
        }
        if (pid == 0) {
          pid = process.th32ProcessID;
          if (actualDetectedPath) *actualDetectedPath = normalizedImage;
        }
      }
    } while (Process32NextW(snapshot, &process));
  }
  CloseHandle(snapshot);
  return pid;
}

void requestAutoInject() {
  gAutoInjectPending = true;
  gPendingSkyPid = 0;
  gPendingSkyTicks = 0;
}

void autoInjectViaController(HWND hwnd, DWORD pid) {
  std::vector<std::wstring> activeDlls;
  for (const auto& item : gPlugins) {
    if (item.enabled) {
      activeDlls.push_back(item.path);
    }
  }
  const std::wstring bootstrap = gBootstrapPath;
  std::thread([pid, bootstrap, activeDlls, hwnd]() {
    LoaderController controller(bootstrap, activeDlls);
    const LoaderResult result = controller.installBootstrapAndPlugins(pid);
    std::wstring* msg = new std::wstring(result.message + L" (PID " + std::to_wstring(pid) + L").");
    PostMessageW(hwnd, WM_INJECTION_COMPLETE, reinterpret_cast<WPARAM>(msg), result.ok ? 1 : 0);
  }).detach();
}

LRESULT CALLBACK launchButtonSubclass(HWND hwnd, UINT msg, WPARAM wParam, LPARAM lParam) {
  switch (msg) {
    case WM_MOUSEMOVE: {
      if (!gLaunchHover) {
        gLaunchHover = true;
        TRACKMOUSEEVENT tme{};
        tme.cbSize = sizeof(TRACKMOUSEEVENT);
        tme.dwFlags = TME_LEAVE;
        tme.hwndTrack = hwnd;
        TrackMouseEvent(&tme);
        InvalidateRect(hwnd, nullptr, TRUE);
      }
      break;
    }
    case WM_MOUSELEAVE: {
      gLaunchHover = false;
      InvalidateRect(hwnd, nullptr, TRUE);
      break;
    }
  }
  return CallWindowProcW(gOrigLaunchBtnProc, hwnd, msg, wParam, lParam);
}

LRESULT CALLBACK addDllButtonSubclass(HWND hwnd, UINT msg, WPARAM wParam, LPARAM lParam) {
  switch (msg) {
    case WM_MOUSEMOVE: {
      if (!gAddDllHover) {
        gAddDllHover = true;
        TRACKMOUSEEVENT tme{};
        tme.cbSize = sizeof(TRACKMOUSEEVENT);
        tme.dwFlags = TME_LEAVE;
        tme.hwndTrack = hwnd;
        TrackMouseEvent(&tme);
        InvalidateRect(hwnd, nullptr, TRUE);
      }
      break;
    }
    case WM_MOUSELEAVE: {
      gAddDllHover = false;
      InvalidateRect(hwnd, nullptr, TRUE);
      break;
    }
  }
  return CallWindowProcW(gOrigAddDllBtnProc, hwnd, msg, wParam, lParam);
}

void layout(HWND window) {
  RECT client{};
  GetClientRect(window, &client);
  const int width = client.right;
  const int height = client.bottom;

  const int margin = 16;
  const int footerHeight = 100;

  // Header row
  const int btnHeight = 28;
  const int addBtnW = 100;
  const int openFolderBtnW = 100;
  const int removeBtnW = 85;
  const int updateBtnW = 115;

  MoveWindow(gPluginsTitle, margin, 18, 150, 20, TRUE);
  MoveWindow(gCheckUpdatesBtn, width - margin - updateBtnW, 14, updateBtnW, btnHeight, TRUE);
  MoveWindow(gOpenFolderBtn, width - margin - updateBtnW - 8 - openFolderBtnW, 14, openFolderBtnW, btnHeight, TRUE);
  MoveWindow(gRemoveDllBtn, width - margin - updateBtnW - 8 - openFolderBtnW - 8 - removeBtnW, 14, removeBtnW, btnHeight, TRUE);
  MoveWindow(gAddDllBtn, width - margin - removeBtnW - 8 - openFolderBtnW - 8 - updateBtnW - 8 - addBtnW, 14, addBtnW, btnHeight, TRUE);

  // Plugins list
  const int listTop = 50;
  const int listHeight = height - listTop - footerHeight - 26;
  MoveWindow(gDllList, margin, listTop, width - margin * 2, listHeight, TRUE);

  // Resize columns
  const int listWidth = width - margin * 2;
  const int versionColW = 110;
  const int authorColW = 150;
  const int descriptionColW = 230;
  const int nameColW = (std::max)(120, listWidth - versionColW - authorColW - descriptionColW - 25);
  ListView_SetColumnWidth(gDllList, 0, nameColW);
  ListView_SetColumnWidth(gDllList, 1, versionColW);
  ListView_SetColumnWidth(gDllList, 2, authorColW);
  ListView_SetColumnWidth(gDllList, 3, descriptionColW);

  // Hint text
  MoveWindow(gHintText, margin, listTop + listHeight + 6, width - margin * 2, 18, TRUE);

  // Footer section
  const int footerTop = height - footerHeight;
  const int launchBtnW = 160;
  const int launchBtnH = 46;

  // Bottom-Right: Launch Button
  MoveWindow(gLaunchBtn, width - margin - launchBtnW, footerTop + 30, launchBtnW, launchBtnH, TRUE);

  // Bottom-Left: Game path & status
  const int leftSectionW = (width - margin - launchBtnW - 16) - margin;
  MoveWindow(gGamePathLabel, margin, footerTop + 10, leftSectionW, 16, TRUE);

  const int browseBtnW = 85;
  const int editW = leftSectionW - browseBtnW - 8;
  MoveWindow(gGamePath, margin, footerTop + 28, editW, 26, TRUE);
  MoveWindow(gBrowseBtn, margin + editW + 8, footerTop + 28, browseBtnW, 26, TRUE);

  MoveWindow(gStatus, margin, footerTop + 64, leftSectionW, 22, TRUE);
}

LRESULT CALLBACK windowProc(HWND window, UINT message, WPARAM wParam, LPARAM lParam) {
  switch (message) {
    case WM_CREATE: {
      gMainWindow = window;
      INITCOMMONCONTROLSEX controls{};
      controls.dwSize = sizeof(controls);
      controls.dwICC = ICC_LISTVIEW_CLASSES;
      InitCommonControlsEx(&controls);

      DragAcceptFiles(window, TRUE);

      // Create Fonts (Segoe UI)
      gFontRegular = CreateFontW(-12, 0, 0, 0, FW_NORMAL, FALSE, FALSE, FALSE,
                                 DEFAULT_CHARSET, OUT_DEFAULT_PRECIS, CLIP_DEFAULT_PRECIS,
                                 CLEARTYPE_QUALITY, DEFAULT_PITCH | FF_DONTCARE, L"Segoe UI");
      gFontSemibold = CreateFontW(-12, 0, 0, 0, FW_SEMIBOLD, FALSE, FALSE, FALSE,
                                  DEFAULT_CHARSET, OUT_DEFAULT_PRECIS, CLIP_DEFAULT_PRECIS,
                                  CLEARTYPE_QUALITY, DEFAULT_PITCH | FF_DONTCARE, L"Segoe UI");
      gFontBold = CreateFontW(-13, 0, 0, 0, FW_BOLD, FALSE, FALSE, FALSE,
                              DEFAULT_CHARSET, OUT_DEFAULT_PRECIS, CLIP_DEFAULT_PRECIS,
                              CLEARTYPE_QUALITY, DEFAULT_PITCH | FF_DONTCARE, L"Segoe UI");
      gFontLaunchBtn = CreateFontW(-14, 0, 0, 0, FW_BOLD, FALSE, FALSE, FALSE,
                                   DEFAULT_CHARSET, OUT_DEFAULT_PRECIS, CLIP_DEFAULT_PRECIS,
                                   CLEARTYPE_QUALITY, DEFAULT_PITCH | FF_DONTCARE, L"Segoe UI");

      gBgBrush = CreateSolidBrush(RGB(248, 250, 252));      // Slate-50
      gFooterBrush = CreateSolidBrush(RGB(255, 255, 255)); // Crisp White

      // Header controls
      gPluginsTitle = CreateWindowW(L"STATIC", L"PLUGINS", WS_CHILD | WS_VISIBLE,
                                    0, 0, 0, 0, window, nullptr, nullptr, nullptr);
      SendMessageW(gPluginsTitle, WM_SETFONT, reinterpret_cast<WPARAM>(gFontBold), TRUE);

      gAddDllBtn = CreateWindowW(L"BUTTON", L"Add DLL...",
                                 WS_CHILD | WS_VISIBLE | WS_TABSTOP | BS_OWNERDRAW,
                                 0, 0, 0, 0, window, reinterpret_cast<HMENU>(kIdAddDll), nullptr, nullptr);
      gOrigAddDllBtnProc = reinterpret_cast<WNDPROC>(SetWindowLongPtrW(
          gAddDllBtn, GWLP_WNDPROC, reinterpret_cast<LONG_PTR>(addDllButtonSubclass)));

      gOpenFolderBtn = CreateWindowW(L"BUTTON", L"Open Folder", WS_CHILD | WS_VISIBLE | WS_TABSTOP,
                                     0, 0, 0, 0, window, reinterpret_cast<HMENU>(kIdOpenFolder), nullptr, nullptr);
      SendMessageW(gOpenFolderBtn, WM_SETFONT, reinterpret_cast<WPARAM>(gFontRegular), TRUE);

      gRemoveDllBtn = CreateWindowW(L"BUTTON", L"Remove", WS_CHILD | WS_VISIBLE | WS_TABSTOP,
                                    0, 0, 0, 0, window, reinterpret_cast<HMENU>(kIdRemoveDll), nullptr, nullptr);
      SendMessageW(gRemoveDllBtn, WM_SETFONT, reinterpret_cast<WPARAM>(gFontRegular), TRUE);

      gCheckUpdatesBtn = CreateWindowW(L"BUTTON", L"Check Updates", WS_CHILD | WS_VISIBLE | WS_TABSTOP,
                                       0, 0, 0, 0, window, reinterpret_cast<HMENU>(kIdCheckUpdates), nullptr, nullptr);
      SendMessageW(gCheckUpdatesBtn, WM_SETFONT, reinterpret_cast<WPARAM>(gFontRegular), TRUE);

      // Plugins ListView
      gDllList = CreateWindowExW(WS_EX_CLIENTEDGE, WC_LISTVIEWW, L"",
                                 WS_CHILD | WS_VISIBLE | WS_TABSTOP | LVS_REPORT | LVS_SINGLESEL,
                                 0, 0, 0, 0, window, reinterpret_cast<HMENU>(kIdDllList), nullptr, nullptr);
      ListView_SetExtendedListViewStyle(gDllList,
                                        LVS_EX_FULLROWSELECT | LVS_EX_DOUBLEBUFFER | LVS_EX_CHECKBOXES | LVS_EX_GRIDLINES);
      SendMessageW(gDllList, WM_SETFONT, reinterpret_cast<WPARAM>(gFontRegular), TRUE);

      LVCOLUMNW col0{};
      col0.mask = LVCF_TEXT | LVCF_WIDTH;
      col0.pszText = const_cast<wchar_t*>(L"Plugin");
      col0.cx = 260;
      ListView_InsertColumn(gDllList, 0, &col0);

      LVCOLUMNW col1{};
      col1.mask = LVCF_TEXT | LVCF_WIDTH;
      col1.pszText = const_cast<wchar_t*>(L"Version");
      col1.cx = 110;
      ListView_InsertColumn(gDllList, 1, &col1);

      LVCOLUMNW col2{};
      col2.mask = LVCF_TEXT | LVCF_WIDTH;
      col2.pszText = const_cast<wchar_t*>(L"Author");
      col2.cx = 150;
      ListView_InsertColumn(gDllList, 2, &col2);

      LVCOLUMNW col3{};
      col3.mask = LVCF_TEXT | LVCF_WIDTH;
      col3.pszText = const_cast<wchar_t*>(L"Description");
      col3.cx = 230;
      ListView_InsertColumn(gDllList, 3, &col3);

      // Drag and drop hint
      gHintText = CreateWindowW(L"STATIC", L"Tip: You can drag and drop .dll files directly into this window.",
                                WS_CHILD | WS_VISIBLE, 0, 0, 0, 0, window, nullptr, nullptr, nullptr);
      SendMessageW(gHintText, WM_SETFONT, reinterpret_cast<WPARAM>(gFontRegular), TRUE);

      // Footer controls
      gGamePathLabel = CreateWindowW(L"STATIC", L"Sky Executable:", WS_CHILD | WS_VISIBLE,
                                     0, 0, 0, 0, window, nullptr, nullptr, nullptr);
      SendMessageW(gGamePathLabel, WM_SETFONT, reinterpret_cast<WPARAM>(gFontSemibold), TRUE);

      gGamePath = CreateWindowExW(WS_EX_CLIENTEDGE, L"EDIT", L"",
                                  WS_CHILD | WS_VISIBLE | WS_TABSTOP | ES_AUTOHSCROLL,
                                  0, 0, 0, 0, window, reinterpret_cast<HMENU>(kIdGamePath), nullptr, nullptr);
      SendMessageW(gGamePath, WM_SETFONT, reinterpret_cast<WPARAM>(gFontRegular), TRUE);

      gBrowseBtn = CreateWindowW(L"BUTTON", L"Browse...", WS_CHILD | WS_VISIBLE | WS_TABSTOP,
                                 0, 0, 0, 0, window, reinterpret_cast<HMENU>(kIdBrowseGame), nullptr, nullptr);
      SendMessageW(gBrowseBtn, WM_SETFONT, reinterpret_cast<WPARAM>(gFontRegular), TRUE);

      // Primary Launch Sky button (Owner-drawn for flat Steam blue appearance)
      gLaunchBtn = CreateWindowW(L"BUTTON", L"LAUNCH SKY",
                                 WS_CHILD | WS_VISIBLE | WS_TABSTOP | BS_OWNERDRAW,
                                 0, 0, 0, 0, window, reinterpret_cast<HMENU>(kIdLaunchGame), nullptr, nullptr);
      gOrigLaunchBtnProc = reinterpret_cast<WNDPROC>(SetWindowLongPtrW(
          gLaunchBtn, GWLP_WNDPROC, reinterpret_cast<LONG_PTR>(launchButtonSubclass)));

      gStatus = CreateWindowW(L"STATIC", L"Ready. Start Sky from Steam or click Launch Sky.",
                              WS_CHILD | WS_VISIBLE, 0, 0, 0, 0, window,
                              reinterpret_cast<HMENU>(kIdStatus), nullptr, nullptr);
      SendMessageW(gStatus, WM_SETFONT, reinterpret_cast<WPARAM>(gFontSemibold), TRUE);

      loadSettings();
      requestAutoInject();
      startUpdateCheck(window, false);
      SetTimer(window, kAutoInjectTimer, kAutoInjectPollMs, nullptr);
      return 0;
    }

    case WM_SIZE:
      layout(window);
      return 0;

    case WM_GETMINMAXINFO: {
      LPMINMAXINFO mmi = reinterpret_cast<LPMINMAXINFO>(lParam);
      mmi->ptMinTrackSize.x = 640;
      mmi->ptMinTrackSize.y = 450;
      return 0;
    }

    case WM_ERASEBKGND:
      return 1;

    case WM_PAINT: {
      PAINTSTRUCT ps{};
      HDC hdc = BeginPaint(window, &ps);
      RECT client{};
      GetClientRect(window, &client);

      const int footerTop = client.bottom - 100;

      // Fill top area with Slate-50
      RECT topRect{0, 0, client.right, footerTop};
      FillRect(hdc, &topRect, gBgBrush);

      // Draw footer background card (white)
      RECT footerRect{0, footerTop, client.right, client.bottom};
      FillRect(hdc, &footerRect, gFooterBrush);

      // Draw 1px crisp separator line above footer (Slate-200)
      HPEN pen = CreatePen(PS_SOLID, 1, RGB(226, 232, 240));
      HGDIOBJ oldPen = SelectObject(hdc, pen);
      MoveToEx(hdc, 0, footerTop, nullptr);
      LineTo(hdc, client.right, footerTop);
      SelectObject(hdc, oldPen);
      DeleteObject(pen);

      EndPaint(window, &ps);
      return 0;
    }

    case WM_DRAWITEM: {
      LPDRAWITEMSTRUCT dis = reinterpret_cast<LPDRAWITEMSTRUCT>(lParam);
      if (dis->CtlID == kIdAddDll) {
        const bool isDown = (dis->itemState & ODS_SELECTED);
        COLORREF fillCol = isDown ? RGB(7, 89, 133)
                                  : (gAddDllHover ? RGB(3, 105, 161) : RGB(2, 132, 199));

        HBRUSH brush = CreateSolidBrush(fillCol);
        FillRect(dis->hDC, &dis->rcItem, brush);
        DeleteObject(brush);

        HPEN borderPen = CreatePen(PS_SOLID, 1, isDown ? RGB(7, 89, 133) : RGB(2, 132, 199));
        HGDIOBJ oldPen = SelectObject(dis->hDC, borderPen);
        HGDIOBJ oldBrush = SelectObject(dis->hDC, GetStockObject(NULL_BRUSH));
        Rectangle(dis->hDC, dis->rcItem.left, dis->rcItem.top, dis->rcItem.right, dis->rcItem.bottom);
        SelectObject(dis->hDC, oldPen);
        SelectObject(dis->hDC, oldBrush);
        DeleteObject(borderPen);

        SetBkMode(dis->hDC, TRANSPARENT);
        SetTextColor(dis->hDC, RGB(255, 255, 255));
        HGDIOBJ oldFont = SelectObject(dis->hDC, gFontSemibold);
        RECT textRect = dis->rcItem;
        if (isDown) OffsetRect(&textRect, 1, 1);
        DrawTextW(dis->hDC, L"Add DLL...", -1, &textRect, DT_CENTER | DT_VCENTER | DT_SINGLELINE);
        SelectObject(dis->hDC, oldFont);
        return TRUE;
      }

      if (dis->CtlID == kIdLaunchGame) {
        const bool isDown = (dis->itemState & ODS_SELECTED);
        COLORREF fillCol = isDown ? RGB(7, 89, 133)
                                  : (gLaunchHover ? RGB(3, 105, 161) : RGB(2, 132, 199));

        HBRUSH brush = CreateSolidBrush(fillCol);
        FillRect(dis->hDC, &dis->rcItem, brush);
        DeleteObject(brush);

        HPEN borderPen = CreatePen(PS_SOLID, 1, isDown ? RGB(7, 89, 133) : RGB(2, 132, 199));
        HGDIOBJ oldPen = SelectObject(dis->hDC, borderPen);
        HGDIOBJ oldBrush = SelectObject(dis->hDC, GetStockObject(NULL_BRUSH));
        Rectangle(dis->hDC, dis->rcItem.left, dis->rcItem.top, dis->rcItem.right, dis->rcItem.bottom);
        SelectObject(dis->hDC, oldPen);
        SelectObject(dis->hDC, oldBrush);
        DeleteObject(borderPen);

        SetBkMode(dis->hDC, TRANSPARENT);
        SetTextColor(dis->hDC, RGB(255, 255, 255));
        HGDIOBJ oldFont = SelectObject(dis->hDC, gFontLaunchBtn);
        RECT textRect = dis->rcItem;
        if (isDown) OffsetRect(&textRect, 1, 1);
        DrawTextW(dis->hDC, L"LAUNCH SKY", -1, &textRect, DT_CENTER | DT_VCENTER | DT_SINGLELINE);
        SelectObject(dis->hDC, oldFont);
        return TRUE;
      }
      break;
    }

    case WM_CTLCOLORSTATIC: {
      HDC hdc = reinterpret_cast<HDC>(wParam);
      HWND hwndCtl = reinterpret_cast<HWND>(lParam);
      SetBkMode(hdc, TRANSPARENT);

      if (hwndCtl == gHintText) {
        SetTextColor(hdc, RGB(100, 116, 139)); // Slate-500
        return reinterpret_cast<INT_PTR>(gBgBrush);
      } else if (hwndCtl == gPluginsTitle) {
        SetTextColor(hdc, RGB(71, 85, 105)); // Slate-600
        return reinterpret_cast<INT_PTR>(gBgBrush);
      } else if (hwndCtl == gStatus) {
        SetTextColor(hdc, RGB(2, 132, 199)); // Sky-600
        return reinterpret_cast<INT_PTR>(gFooterBrush);
      } else if (hwndCtl == gGamePathLabel) {
        SetTextColor(hdc, RGB(30, 41, 59)); // Slate-800
        return reinterpret_cast<INT_PTR>(gFooterBrush);
      }
      return reinterpret_cast<INT_PTR>(gBgBrush);
    }

    case WM_INJECTION_COMPLETE: {
      std::unique_ptr<std::wstring> msg(reinterpret_cast<std::wstring*>(wParam));
      if (msg) {
        setStatus(*msg);
      }
      return 0;
    }

    case WM_UPDATE_CHECK_COMPLETE: {
      std::unique_ptr<UpdateCheckResult> result(reinterpret_cast<UpdateCheckResult*>(wParam));
      gUpdateCheckInProgress = false;
      if (!result) return 0;
      if (result->ok && result->updateAvailable) {
        setStatus(result->message);
        const std::wstring prompt = L"SkyLoader " + result->latestVersion +
          L" has been downloaded.\n\nRun the downloaded updater now?";
        if (MessageBoxW(window, prompt.c_str(), L"Update Available",
                        MB_YESNO | MB_ICONINFORMATION) == IDYES) {
          const HINSTANCE launched = ShellExecuteW(window, L"open",
                                                   result->downloadedPath.c_str(),
                                                   nullptr, nullptr, SW_SHOWNORMAL);
          if (reinterpret_cast<INT_PTR>(launched) <= 32) {
            setStatus(L"Downloaded update could not be started.");
            MessageBoxW(window, L"The update was downloaded, but Windows could not start it.",
                        L"SkyLoader Updates", MB_ICONWARNING);
          }
        }
      } else if (result->manual) {
        const std::wstring text = result->message.empty()
          ? L"Could not check for updates."
          : result->message;
        setStatus(text);
        MessageBoxW(window, text.c_str(), L"SkyLoader Updates",
                    result->ok ? MB_ICONINFORMATION : MB_ICONWARNING);
      }
      return 0;
    }

    case WM_NOTIFY: {
      LPNMHDR hdr = reinterpret_cast<LPNMHDR>(lParam);
      if (hdr->idFrom == kIdDllList) {
        if (hdr->code == LVN_ITEMCHANGED && !gIgnoreItemChanged) {
          NMLISTVIEW* pnmv = reinterpret_cast<NMLISTVIEW*>(lParam);
          if (pnmv->uChanged & LVIF_STATE) {
            const UINT oldState = pnmv->uOldState & LVIS_STATEIMAGEMASK;
            const UINT newState = pnmv->uNewState & LVIS_STATEIMAGEMASK;
            if (oldState != newState && newState != 0) {
              const bool checked = ((newState >> 12) == 2);
              if (pnmv->iItem >= 0 && static_cast<size_t>(pnmv->iItem) < gPlugins.size()) {
                gPlugins[pnmv->iItem].enabled = checked;
                ListView_SetItemText(gDllList, pnmv->iItem, 1,
                                     const_cast<wchar_t*>(checked ? L"Enabled" : L"Disabled"));
                saveSettings();

                wchar_t configuredPath[MAX_PATH]{};
                GetWindowTextW(gGamePath, configuredPath, MAX_PATH);
                const DWORD runningPid = findSkyProcess(configuredPath);
                if (runningPid) {
                  const std::wstring pluginPath = gPlugins[pnmv->iItem].path;
                  const std::wstring bootstrap = gBootstrapPath;
                  std::thread([runningPid, bootstrap, pluginPath, checked, window]() {
                    LoaderController controller(bootstrap, {});
                    LoaderResult res = checked ? controller.loadPlugin(runningPid, pluginPath)
                                               : controller.unloadPlugin(runningPid, pluginPath);
                    std::wstring* msg = new std::wstring(res.message);
                    PostMessageW(window, WM_INJECTION_COMPLETE, reinterpret_cast<WPARAM>(msg), res.ok ? 1 : 0);
                  }).detach();
                }
              }
            }
          }
        } else if (hdr->code == NM_DBLCLK) {
          const int selected = ListView_GetNextItem(gDllList, -1, LVNI_SELECTED);
          if (selected >= 0 && static_cast<size_t>(selected) < gPlugins.size()) {
            const BOOL current = ListView_GetCheckState(gDllList, selected);
            ListView_SetCheckState(gDllList, selected, !current);
          }
        }
      }
      break;
    }

    case WM_DROPFILES: {
      HDROP hDrop = reinterpret_cast<HDROP>(wParam);
      const UINT count = DragQueryFileW(hDrop, 0xFFFFFFFF, nullptr, 0);
      bool anyAdded = false;
      for (UINT i = 0; i < count; ++i) {
        wchar_t filePath[MAX_PATH]{};
        if (DragQueryFileW(hDrop, i, filePath, MAX_PATH)) {
          const std::wstring path = normalizedPath(filePath);
          if (path.size() >= 4 &&
              CompareStringOrdinal(path.c_str() + path.size() - 4, -1, L".dll", -1, TRUE) == CSTR_EQUAL) {
            std::wstring importMessage;
            const PluginImportAction action = importAndRegisterPlugin(window, path, importMessage);
            if (action == PluginImportAction::Added || action == PluginImportAction::Updated)
              anyAdded = true;
            else if (action == PluginImportAction::Failed)
              setStatus(importMessage);
          }
        }
      }
      DragFinish(hDrop);
      if (anyAdded) {
        populateDllList();
        saveSettings();
        setStatus(L"Plugin(s) added via drag and drop.");
      }
      return 0;
    }

    case WM_COMMAND: {
      switch (LOWORD(wParam)) {
        case kIdBrowseGame: {
          std::wstring path;
          if (chooseFile(window, L"Sky executable\0Sky.exe\0Executables\0*.exe\0\0", path)) {
            SetWindowTextW(gGamePath, path.c_str());
            saveSettings();
          }
          return 0;
        }
        case kIdLaunchGame: {
          wchar_t configuredPath[MAX_PATH]{};
          GetWindowTextW(gGamePath, configuredPath, MAX_PATH);
          std::wstring actualPath;
          const DWORD runningPid = findSkyProcess(configuredPath, &actualPath);
          requestAutoInject();
          if (runningPid) {
            if (!actualPath.empty() && !samePath(configuredPath, actualPath)) {
              SetWindowTextW(gGamePath, actualPath.c_str());
            }
            gPendingSkyPid = runningPid;
            gPendingSkyTicks = 0;
            setStatus(L"Sky.exe is already running; loading Bootstrap now.");
            gAutoInjectPending = false;
            autoInjectViaController(window, runningPid);
            return 0;
          }
          const HINSTANCE result = ShellExecuteW(window, L"open", kSkySteamUri, nullptr, nullptr, SW_SHOWNORMAL);
          setStatus(reinterpret_cast<INT_PTR>(result) <= 32
            ? L"Could not open Steam. Install or start Steam and try again."
            : L"Steam was asked to start Sky. Waiting for Sky.exe...");
          saveSettings();
          return 0;
        }
        case kIdAddDll: {
          std::wstring path;
          if (chooseFile(window, L"Dynamic-link libraries (*.dll)\0*.dll\0\0", path)) {
            std::wstring importMessage;
            const PluginImportAction action = importAndRegisterPlugin(window, path, importMessage);
            if (action == PluginImportAction::Added || action == PluginImportAction::Updated) {
              populateDllList();
              saveSettings();
            }
            setStatus(importMessage);
          }
          return 0;
        }
        case kIdOpenFolder: {
          const std::wstring dir = managedPluginDirectory();
          if (!dir.empty()) {
            ShellExecuteW(window, L"open", dir.c_str(), nullptr, nullptr, SW_SHOWNORMAL);
          }
          return 0;
        }
        case kIdCheckUpdates:
          startUpdateCheck(window, true);
          return 0;
        case kIdRemoveDll: {
          const int selected = ListView_GetNextItem(gDllList, -1, LVNI_SELECTED);
          if (selected < 0 || static_cast<size_t>(selected) >= gPlugins.size()) {
            setStatus(L"Select a plugin first.");
          } else {
            const std::wstring removedPath = gPlugins[selected].path;
            gPlugins.erase(gPlugins.begin() + selected);
            populateDllList();
            saveSettings();
            setStatus(L"Plugin removed from the list; original file was not deleted.");

            wchar_t configuredPath[MAX_PATH]{};
            GetWindowTextW(gGamePath, configuredPath, MAX_PATH);
            const DWORD runningPid = findSkyProcess(configuredPath);
            if (runningPid) {
              const std::wstring bootstrap = gBootstrapPath;
              std::thread([runningPid, bootstrap, removedPath, window]() {
                LoaderController controller(bootstrap, {});
                LoaderResult res = controller.unloadPlugin(runningPid, removedPath);
                std::wstring* msg = new std::wstring(res.message);
                PostMessageW(window, WM_INJECTION_COMPLETE, reinterpret_cast<WPARAM>(msg), res.ok ? 1 : 0);
              }).detach();
            }
          }
          return 0;
        }
      }
      break;
    }

    case WM_TIMER:
      if (wParam == kAutoInjectTimer && gAutoInjectPending) {
        wchar_t configuredPath[MAX_PATH]{};
        GetWindowTextW(gGamePath, configuredPath, MAX_PATH);
        std::wstring actualPath;
        const DWORD currentPid = findSkyProcess(configuredPath, &actualPath);
        if (!currentPid) {
          setStatus(L"Waiting for Steam to start Sky.exe...");
          return 0;
        }
        if (!actualPath.empty() && !samePath(configuredPath, actualPath)) {
          SetWindowTextW(gGamePath, actualPath.c_str());
          saveSettings();
        }
        if (currentPid != gPendingSkyPid) {
          gPendingSkyPid = currentPid;
          gPendingSkyTicks = 0;
          setStatus(L"Sky.exe detected; loading Bootstrap DLL...");
        }
        gAutoInjectPending = false;
        autoInjectViaController(window, currentPid);
      }
      return 0;

    case WM_DESTROY:
      KillTimer(window, kAutoInjectTimer);
      saveSettings();

      if (gFontRegular) DeleteObject(gFontRegular);
      if (gFontSemibold) DeleteObject(gFontSemibold);
      if (gFontBold) DeleteObject(gFontBold);
      if (gFontLaunchBtn) DeleteObject(gFontLaunchBtn);
      if (gBgBrush) DeleteObject(gBgBrush);
      if (gFooterBrush) DeleteObject(gFooterBrush);

      PostQuitMessage(0);
      return 0;
  }
  return DefWindowProcW(window, message, wParam, lParam);
}

} // namespace

int WINAPI wWinMain(HINSTANCE instance, HINSTANCE, PWSTR, int showCommand) {
  const std::wstring directory = moduleDirectory();
  gIniPath = directory + L"\\SkyLoader.ini";

  WNDCLASSEXW windowClass{};
  windowClass.cbSize = sizeof(windowClass);
  windowClass.hInstance = instance;
  windowClass.lpszClassName = L"SkyLoaderWindow";
  windowClass.lpfnWndProc = windowProc;
  windowClass.hCursor = LoadCursorW(nullptr, IDC_ARROW);
  windowClass.hbrBackground = nullptr; // Handled in WM_ERASEBKGND / WM_PAINT
  windowClass.hIcon = static_cast<HICON>(LoadImageW(instance, L"IDI_SKYLOADER_ICON", IMAGE_ICON,
                                                     GetSystemMetrics(SM_CXICON), GetSystemMetrics(SM_CYICON),
                                                     LR_DEFAULTCOLOR));
  windowClass.hIconSm = static_cast<HICON>(LoadImageW(instance, L"IDI_SKYLOADER_ICON", IMAGE_ICON,
                                                       GetSystemMetrics(SM_CXSMICON), GetSystemMetrics(SM_CYSMICON),
                                                       LR_DEFAULTCOLOR));
  if (!windowClass.hIcon) windowClass.hIcon = LoadIconW(nullptr, IDI_APPLICATION);
  if (!windowClass.hIconSm) windowClass.hIconSm = windowClass.hIcon;
  if (!RegisterClassExW(&windowClass)) return 1;

  HWND window = CreateWindowExW(0, windowClass.lpszClassName, L"SkyLoader (v0.1.1)",
                                WS_OVERLAPPEDWINDOW | WS_CLIPCHILDREN,
                                CW_USEDEFAULT, CW_USEDEFAULT, 780, 540,
                                nullptr, nullptr, instance, nullptr);
  if (!window) return 1;
  ShowWindow(window, showCommand);
  UpdateWindow(window);

  MSG message{};
  while (GetMessageW(&message, nullptr, 0, 0) > 0) {
    TranslateMessage(&message);
    DispatchMessageW(&message);
  }
  return static_cast<int>(message.wParam);
}
