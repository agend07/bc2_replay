# BC2 / Project Rome — server-side match recording

Tools and notes for recording Battlefield: Bad Company 2 dedicated-server
matches (Project Rome), plus the current state of playing them back.

## Recording

BC2 dedicated servers can record a whole match as *ghost data*: every player's
position + orientation (~30 Hz), plus destruction and spotting. The stock
server cannot enable this from the command line, so this repo contains a small
binary patch that turns the recorder on, together with the tools to launch,
monitor and save a recording.

**How to modify the server and run it to record a match:**
see [`summary_pt1.md`](summary_pt1.md).

This repo also ships two recorded matches (`recordings_pt1.zip`) and example
configs.

## Playback — current state

Playback works: the server can replay a recording, and a client can join and
watch the match — with a timeline / seek and a free-fly camera.

What is **not** solved yet is making the replay automatically follow one chosen
player. The viewer is bound to a recorded player by the *server* (likely by
connection slot / order), not by account or persona name — so joining as a
specific player does not make the replay follow them.

**Status: usable manually, not yet automated.** A fuller write-up (Part 2)
exists but has no step-by-step instructions yet, so it is not included here.

## Not included

Game / server binaries (EA / DICE) are not included (copyright). You bring your
own BC2 dedicated server files and Project Rome.