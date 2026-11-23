# MangaHub seed data sources

The 35 records in `internal/database/manga_seed.json` refer to real manga titles, credited creators, and broad series premises. Descriptions are freshly written short summaries rather than copied publisher blurbs.

## Publisher references

- VIZ Shonen Jump catalogue and chapter index: https://www.viz.com/shonenjump
- VIZ free chapter index (titles and chapter numbers): https://www.viz.com/manga-books/shonenjump/section/free-chapters
- VIZ official title and creator credits: https://www.viz.com/copyrights
- VIZ trending manga (ongoing chapter snapshots): https://www.viz.com/manga-books/shonenjump/section/trending-manga
- Kodansha official copyrights and creator credits: https://kodansha.us/copyrights/
- Kodansha Attack on Titan: https://kodansha.us/series/attack-on-titan/
- Kodansha FAIRY TAIL: https://kodansha.us/series/fairy-tail/
- Kodansha Blue Lock: https://kodansha.us/series/blue-lock/
- Kodansha Vinland Saga: https://kodansha.us/series/Vinland-saga/
- VIZ announcement of Sakamoto Days finale: https://www.viz.com/shonenjump

## Important notes

- This is a demonstration catalogue as of 2026-11-23, **not a live manga metadata service**. `total_chapters` are stored starting values for tracking progress. Chapter numbering can differ by publisher, edition, bonuses, and later releases. In particular, Blue Lock is seeded at a conservative demonstration baseline of 300, not claimed as the live latest chapter count.
- The final chapter count for a completed main story may omit extras, bonus chapters, epilogues, or special releases.
- Running `SeedCatalog` against a non-empty catalog inserts nothing and does not change any pre-existing chapters or other metadata.
- MangaHub stores only basic metadata and original short summaries; it does **not** store or distribute copyrighted manga pages or scans.
