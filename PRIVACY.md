# Privacy

This describes what the StarLance App sends to the StarLance ladder server
and what the server keeps. It is written to be accurate, not flattering.

## What the app sends

- **Replays.** Every StarCraft II game the app uploads is sent in full to the
  ladder server. A replay file contains the game itself: player names,
  Battle.net tags, player and account identifiers, chat, build orders, and
  every action taken in the game.
- **A login token.** Issued through the browser once you approve the app on
  the StarLance website. The app stores it on your machine with user-only
  permissions and sends it with uploads and heartbeats.
- **A heartbeat.** While running, the app periodically tells the server the
  uploader is alive so the queue can show your systems check.

The app has no analytics, no telemetry, and no crash reporting.

## What the server keeps

- Uploaded replay files, indefinitely. There is no expiry and no automatic
  deletion.
- Your player name, Battle.net tag, and StarCraft II account and character
  identifiers.
- Your queue entries, matches, results, ratings, and map bans.
- IP addresses in server logs, kept for abuse prevention.

## Who can see it

Public replays are downloadable by anyone who has the link. A player can turn
public replays off in the app's settings; a replay is private when either
player had public replays off at match time. Private replays are downloadable
only by the two players and StarLance admins. Profiles, match history, match
results, and ratings are public on the StarLance website. Your login token and
email, if you ever provide one, are not public.

## Deleting data

There is no account deletion feature yet. To ask for removal of your data,
email sc2-starlance@proton.me and it will be handled by hand.

## What stays on your machine

Login token, settings, the list of already-uploaded replays, and the log, all
under the per-user state folder described in the README. The log records
upload outcomes and errors, not replay contents.

## Contact

sc2-starlance@proton.me
