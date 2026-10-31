/* In-game chat commands for BFBC2Game.exe (r255), ported from the bc2_mute msimg32.dll proxy into the shared
 * dinput8 proxy: three instance-vtable clone hooks (no .text writes) catch incoming chat (mute filter) and the
 * outgoing chat submit (slash commands consumed locally, never sent to the server).
 * Commands: /mute /unmute /mutelist, /hash /unhash /hashlist (a hashed sender's text shows as symbols, same length),
 * /rec status|off|on, /smoke <color|off|status>, /help.
 * ON by default; off with %LOCALAPPDATA%\BC2Replay\config.ini [dll] chat=off. Lists: %LOCALAPPDATA%\BC2Replay\
 * muted_players.txt / hashed_players.txt (hot-reloaded every 2 s). Log: %LOCALAPPDATA%\BC2Replay\logs\chat_<pid>.log.
 * INSTALL NOTE: the old msimg32.dll (bc2_mute) must be removed from the game folder, or both DLLs swap the same vtables.
 */
#ifndef BC2_CLIENT_CHAT_H
#define BC2_CLIENT_CHAT_H

/* 0 only when the ini has [dll] chat=off. */
int bc2_client_chat_enabled(void);

/* Called from bc2_mode_attach (client process, DllMain attach): starts the 250 ms hook-monitor thread if enabled.
 * The thread checks the vtable bytes first and stays inactive on an unknown exe. */
void bc2_client_chat_attach(void);

/* Called from DllMain(DLL_PROCESS_DETACH): sets the stop flag, never waits. */
void bc2_client_chat_detach(void);

/* Pure command layer (selftest-able without the game): handles one submitted chat line (UTF-8, leading spaces ok).
 * Returns 1 when the line was a local command (reply in out, shown only to the local player; '\n' splits it into
 * several chat messages), 0 = not a command
 * (send to the server as usual). */
int bc2_chat_command(const char *utf8_line, char *out, int out_size);

/* List access (selftest): file paths (NULL = default), membership, and the /hash same-length text transform. */
void bc2_chat_set_mute_file(const char *path);
void bc2_chat_set_hash_file(const char *path);
int bc2_chat_is_muted(const char *sender, const char *first_word);
int bc2_chat_is_hashed(const char *sender);
void bc2_chat_hash_text(char *s, unsigned len);

/* Append one line to the chat log file (logs\chat_<pid>.log) and OutputDebugString; any thread. */
void bc2_chat_log(const char *text);

#endif
