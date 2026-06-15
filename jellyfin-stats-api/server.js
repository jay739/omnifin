const express = require("express");
const axios = require("axios");
const { Pool } = require("pg");
const app = express();
const PORT = 3020;

// Jellyfin server configuration
const JELLYFIN_URL = "http://jellyfin:8096";
const JELLYFIN_API_KEY =
  process.env.JELLYFIN_API_KEY || "YOUR_JELLYFIN_API_KEY";
const JELLYSTAT_DB_ENABLED = process.env.JELLYSTAT_DB_ENABLED !== "false";
const jellystatPool = new Pool({
  host: process.env.JELLYSTAT_DB_HOST || "jellystat-db",
  port: parseInt(process.env.JELLYSTAT_DB_PORT || "5432", 10),
  database: process.env.JELLYSTAT_DB_NAME || "jfstat",
  user: process.env.JELLYSTAT_DB_USER || "jellystat",
  password: process.env.JELLYSTAT_DB_PASSWORD || "",
  max: 3,
  idleTimeoutMillis: 30000,
  connectionTimeoutMillis: 3000,
});

// Enable CORS
app.use((req, res, next) => {
  res.header("Access-Control-Allow-Origin", "*");
  res.header("Access-Control-Allow-Methods", "GET, POST, OPTIONS");
  res.header(
    "Access-Control-Allow-Headers",
    "Origin, X-Requested-With, Content-Type, Accept, Authorization",
  );
  res.header("Access-Control-Allow-Credentials", "true");
  if (req.method === "OPTIONS") {
    return res.sendStatus(200);
  }
  next();
});

function compactNumber(value) {
  const n = Number(value || 0);
  return new Intl.NumberFormat("en-US").format(Math.round(n));
}

function durationLabel(seconds) {
  const totalMinutes = Math.max(0, Math.round(Number(seconds || 0) / 60));
  const hours = Math.floor(totalMinutes / 60);
  const minutes = totalMinutes % 60;
  if (hours > 0 && minutes > 0) return `${hours}h ${minutes}m`;
  if (hours > 0) return `${hours}h`;
  return `${minutes}m`;
}

function markdownTable(headers, rows, emptyText) {
  if (!rows || rows.length === 0) return emptyText;
  const header = `| ${headers.join(" | ")} |`;
  const divider = `| ${headers.map(() => "---").join(" | ")} |`;
  return [header, divider, ...rows.map((row) => `| ${row.join(" | ")} |`)].join(
    "\n",
  );
}

function statCards(cards) {
  return `<table role="presentation" cellpadding="0" cellspacing="0" border="0" style="width:100%;border-collapse:collapse;">
<tr>
${cards
  .map(
    (
      card,
    ) => `<td style="width:${Math.floor(100 / cards.length)}%;padding:6px;vertical-align:top;" valign="top">
  <div style="border:1px solid rgba(148,163,184,0.25);border-radius:8px;padding:12px;background:rgba(15,23,42,0.35);">
    <div style="font-size:12px;color:#94a3b8;line-height:1.3;">${card.label}</div>
    <div style="font-size:22px;font-weight:700;color:#f8fafc;line-height:1.35;">${card.value}</div>
  </div>
</td>`,
  )
  .join("")}
</tr>
</table>`;
}

// Jellystat-backed community stats for Omnifin announcement templates.
app.get("/api/announcement-summary", async (req, res) => {
  const days = Math.max(
    1,
    Math.min(parseInt(req.query.days || "30", 10) || 30, 365),
  );
  if (!JELLYSTAT_DB_ENABLED) {
    return res.json({ source: "disabled", vars: {} });
  }

  try {
    const [summary, titles, users, clients] = await Promise.all([
      jellystatPool.query(
        `select
                    count(*)::int as plays,
                    count(distinct "UserId")::int as users,
                    coalesce(sum("PlaybackDuration"), 0)::bigint as duration
                 from jf_playback_activity
                 where "ActivityDateInserted" >= now() - ($1::int * interval '1 day')`,
        [days],
      ),
      jellystatPool.query(
        `select
                    coalesce(nullif("SeriesName", ''), "NowPlayingItemName", 'Unknown') as title,
                    count(*)::int as plays,
                    coalesce(sum("PlaybackDuration"), 0)::bigint as duration
                 from jf_playback_activity
                 where "ActivityDateInserted" >= now() - ($1::int * interval '1 day')
                 group by 1
                 order by plays desc, duration desc
                 limit 6`,
        [days],
      ),
      jellystatPool.query(
        `select
                    coalesce(nullif("UserName", ''), 'Unknown') as username,
                    count(*)::int as plays,
                    coalesce(sum("PlaybackDuration"), 0)::bigint as duration,
                    max("ActivityDateInserted") as last_seen
                 from jf_playback_activity
                 where "ActivityDateInserted" >= now() - ($1::int * interval '1 day')
                 group by 1
                 order by duration desc, plays desc
                 limit 6`,
        [days],
      ),
      jellystatPool.query(
        `select
                    coalesce(nullif("Client", ''), 'Unknown') as client,
                    count(*)::int as plays,
                    coalesce(sum("PlaybackDuration"), 0)::bigint as duration
                 from jf_playback_activity
                 where "ActivityDateInserted" >= now() - ($1::int * interval '1 day')
                 group by 1
                 order by plays desc, duration desc
                 limit 5`,
        [days],
      ),
    ]);

    const s = summary.rows[0] || { plays: 0, users: 0, duration: 0 };
    const vars = {
      stats_source: "Jellystat",
      stats_days: String(days),
      watch_plays_30d: compactNumber(s.plays),
      active_watchers_30d: compactNumber(s.users),
      watch_time_30d: durationLabel(s.duration),
      watch_hours_30d: compactNumber(Number(s.duration || 0) / 3600),
      community_stats: statCards([
        { label: `Plays in ${days} days`, value: compactNumber(s.plays) },
        { label: "Active watchers", value: compactNumber(s.users) },
        { label: "Watch time", value: durationLabel(s.duration) },
      ]),
      top_titles_30d: markdownTable(
        ["Title", "Plays", "Watch time"],
        titles.rows.map((r) => [
          r.title,
          compactNumber(r.plays),
          durationLabel(r.duration),
        ]),
        "_(no playback yet)_",
      ),
      top_users_30d: markdownTable(
        ["User", "Plays", "Watch time"],
        users.rows.map((r) => [
          r.username,
          compactNumber(r.plays),
          durationLabel(r.duration),
        ]),
        "_(no active users yet)_",
      ),
      top_clients_30d: markdownTable(
        ["App", "Plays", "Watch time"],
        clients.rows.map((r) => [
          r.client,
          compactNumber(r.plays),
          durationLabel(r.duration),
        ]),
        "_(no app data yet)_",
      ),
    };

    res.json({ source: "jellystat", days, vars });
  } catch (error) {
    console.error(
      "Error fetching Jellystat announcement summary:",
      error.message,
    );
    res.status(503).json({
      source: "jellystat",
      error: "Failed to fetch Jellystat announcement summary",
      message: error.message,
      vars: {},
    });
  }
});

// Get user stats by username
app.get("/api/user-stats/:username", async (req, res) => {
  try {
    const { username } = req.params;

    // Get all users from Jellyfin
    const usersResponse = await axios.get(`${JELLYFIN_URL}/Users`, {
      headers: { "X-Emby-Token": JELLYFIN_API_KEY },
    });

    // Find the user by username
    const user = usersResponse.data.find(
      (u) => u.Name.toLowerCase() === username.toLowerCase(),
    );

    if (!user) {
      return res.status(404).json({ error: "User not found" });
    }

    const userId = user.Id;

    // Fetch user's play count and statistics
    const [itemsResponse, playbackResponse] = await Promise.all([
      // Get user's items
      axios.get(`${JELLYFIN_URL}/Users/${userId}/Items`, {
        headers: { "X-Emby-Token": JELLYFIN_API_KEY },
        params: {
          Recursive: true,
          Fields: "UserData",
          Limit: 10000,
        },
      }),
      // Get recently played items
      axios.get(`${JELLYFIN_URL}/Users/${userId}/Items`, {
        headers: { "X-Emby-Token": JELLYFIN_API_KEY },
        params: {
          Recursive: true,
          IsPlayed: true,
          Fields: "UserData",
          Limit: 10000,
        },
      }),
    ]);

    const allItems = itemsResponse.data.Items || [];
    const playedItems = playbackResponse.data.Items || [];

    // Calculate statistics
    const stats = {
      username: user.Name,
      totalItems: allItems.length,
      playedItems: playedItems.length,
      movies: playedItems.filter((i) => i.Type === "Movie").length,
      episodes: playedItems.filter((i) => i.Type === "Episode").length,
      totalPlayCount: playedItems.reduce(
        (sum, item) => sum + (item.UserData?.PlayCount || 0),
        0,
      ),
      totalWatchTimeMinutes: Math.round(
        playedItems.reduce(
          (sum, item) =>
            sum + (item.RunTimeTicks ? item.RunTimeTicks / 600000000 : 0),
          0,
        ),
      ),
      lastPlayed:
        playedItems.length > 0
          ? playedItems.sort(
              (a, b) =>
                new Date(b.UserData?.LastPlayedDate || 0) -
                new Date(a.UserData?.LastPlayedDate || 0),
            )[0]
          : null,
    };

    res.json(stats);
  } catch (error) {
    console.error("Error fetching user stats:", error.message);
    res.status(500).json({
      error: "Failed to fetch statistics",
      message: error.message,
    });
  }
});

// Get recently watched items
app.get("/api/user-recent/:username", async (req, res) => {
  try {
    const { username } = req.params;
    const limit = parseInt(req.query.limit) || 10;

    // Get all users from Jellyfin
    const usersResponse = await axios.get(`${JELLYFIN_URL}/Users`, {
      headers: { "X-Emby-Token": JELLYFIN_API_KEY },
    });

    const user = usersResponse.data.find(
      (u) => u.Name.toLowerCase() === username.toLowerCase(),
    );

    if (!user) {
      return res.status(404).json({ error: "User not found" });
    }

    // Get recently played
    const recentResponse = await axios.get(
      `${JELLYFIN_URL}/Users/${user.Id}/Items`,
      {
        headers: { "X-Emby-Token": JELLYFIN_API_KEY },
        params: {
          Recursive: true,
          IsPlayed: true,
          SortBy: "DatePlayed",
          SortOrder: "Descending",
          Fields: "Overview,PrimaryImageAspectRatio",
          Limit: limit,
        },
      },
    );

    const recentItems = (recentResponse.data.Items || []).map((item) => ({
      name: item.Name,
      type: item.Type,
      series: item.SeriesName,
      lastPlayed: item.UserData?.LastPlayedDate,
      playCount: item.UserData?.PlayCount || 0,
    }));

    res.json(recentItems);
  } catch (error) {
    console.error("Error fetching recent items:", error.message);
    res.status(500).json({
      error: "Failed to fetch recent items",
      message: error.message,
    });
  }
});

// Per-user watch-time for ALL users, backing the Omnifin accounts column.
// Same grouped Jellystat query as the announcement summary but without the
// top-N cap, returned as a username-keyed map for O(1) lookup per account row.
app.get("/api/user-watchtime", async (req, res) => {
  const days = Math.max(
    1,
    Math.min(parseInt(req.query.days || "30", 10) || 30, 365),
  );
  if (!JELLYSTAT_DB_ENABLED) {
    return res.json({ source: "disabled", days, users: {} });
  }
  try {
    const { rows } = await jellystatPool.query(
      `select
                coalesce(nullif("UserName", ''), 'Unknown') as username,
                count(*)::int as plays,
                coalesce(sum("PlaybackDuration"), 0)::bigint as duration,
                max("ActivityDateInserted") as last_seen
             from jf_playback_activity
             where "ActivityDateInserted" >= now() - ($1::int * interval '1 day')
             group by 1`,
      [days],
    );
    const users = {};
    for (const r of rows) {
      users[r.username] = {
        plays: r.plays,
        watchTimeSeconds: Number(r.duration || 0),
        watchTime: durationLabel(r.duration),
        lastSeen: r.last_seen,
      };
    }
    res.json({ source: "jellystat", days, users });
  } catch (error) {
    console.error("Error fetching per-user watch-time:", error.message);
    res.status(503).json({
      source: "jellystat",
      error: "Failed to fetch per-user watch-time",
      message: error.message,
      users: {},
    });
  }
});

// Health check
app.get("/health", (req, res) => {
  res.json({ status: "ok", service: "jellyfin-stats-api" });
});

app.listen(PORT, "0.0.0.0", () => {
  console.log(`Jellyfin Stats API running on port ${PORT}`);
  console.log(`Jellyfin URL: ${JELLYFIN_URL}`);
});
