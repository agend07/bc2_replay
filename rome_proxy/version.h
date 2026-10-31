/* One release version shared by the panel (preview_config.VERSION), this DLL and the builder
 * (builder.json "version"); release_pack.py refuses a zip whose parts disagree. test_version_sync.py
 * reads BC2_VERSION_STRING from this file, so keep the three defines on their own lines.
 * The panel reads the version of an installed dinput8.dll from the embedded marker
 * "BC2_PROXY_VERSION=<string>\0" (client_install.dll_version), so BC2_VERSION_MARKER must stay
 * referenced by code (bc2_mode_attach logs it) or the optimizer drops it.
 * BC2_SHARED_MARKER identifies this (shared) build in client_install.classify; the literal is kept
 * from r231 builds, whose server patch flag carried that name, so installed builds still match. */
#ifndef BC2_VERSION_H
#define BC2_VERSION_H

#define BC2_VERSION_STRING "0.1.0-preview.4"
#define BC2_DLL_VERSION    2u   /* .bc2raw header field 44 (u32); 1 = r248..r263 builds */
#define BC2_VERSION_MARKER "BC2_PROXY_VERSION=" BC2_VERSION_STRING
#define BC2_SHARED_MARKER  "BC2_REPLAY_PATCH"

#endif
