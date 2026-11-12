# Using opsie

| Key | Does |
|---|---|
| `1`–`9`, `0` | switch tabs |
| `tab` / `shift+tab` | next / previous tab |
| `↑` `↓` / `j` `k` | move through the list; on wide screens, down a column and on to the next |
| `←` `→` / `h` `l` | move to the column beside it, when cards sit side by side |
| `g` `G` / `home` `end` | first / last item |
| `enter` | open the selected item |
| `PgUp` `PgDn` / `b` `f` `space` | scroll the page |
| `s` | on Processes, sort by CPU or memory |
| `r` | check everything again now |
| `?` | help |
| `q` | quit |

In a detail view, a few buttons sit in a row; more than fit become a menu under a rule, one per line or in even columns, with the chosen one highlighted across its cell. The arrows move between them the way they look: `←` `→` along a row (running on to the next), `↑` `↓` to the row above or below. With one row of buttons, `↑` `↓` scroll the text instead. `tab` also moves along them. `enter` or `space` presses it, `1`–`9` press one directly, `PgUp` `PgDn` scroll a long description, and `esc` (or `q`) goes back. When an action asks you to type something, `tab` or `↑` `↓` move between the field and **Cancel**.

In the log viewer, `↑` `↓` scroll (scrolling up stops following), `PgUp` `PgDn` scroll a page, `g` jumps to the oldest lines, `G` to the newest and follows them live, `w` toggles wrapping, `r` reads the log again, and `esc` goes back. Detail views open with a harmless button selected. Anything disruptive asks for confirmation with **Cancel** selected, and there the number keys are ignored, so a stray key or a double Enter can't go ahead by accident.

Everything is also a tap target: checklist lines, containers and cards open, buttons and tabs press, the hints at the bottom work, and swiping scrolls.

## On a phone

Any SSH client that passes touches through as mouse events works: Termius, Blink, JuiceSSH and others. In portrait the cards stack into one scrolling column. The Overview starts with every reading as a line of its own (CPU, memory, each disk, the network; tap one for its history or its tab), then the checklist. The tabs sit at the bottom within reach of your thumb, as a strip of the ones around the tab you're on: tap one, or the arrow at either end for the rest. A popup's actions are a menu of full rows to tap, and rows that would be cut off wrap onto a second line instead. Turn the phone sideways and the cards spread into columns and the tabs move to the top.
