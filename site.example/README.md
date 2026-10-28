# Your site

Everything about one deployment that isn't code: the hub's name, where the bays dial, and what each bay is.  Copy this folder and fill it in:

```
make site            # copies site.example/ to site/, once; git ignores site/
```

`site/` is yours.  Make it its own private repo if you want a history of your fleet: `cd site && git init`.

```
site/
  site.env                    HUB and HUB_HOST, for make
  hub/.env                    -> /opt/zawiper/.env on the hub host
  hub/compose.override.yml    -> /opt/zawiper/compose.override.yml (optional)
  bays/<host>/<bay>.toml      -> /etc/zawiper/bays/<bay>.toml on that host
```

The examples are a two-slot dock on `lab-a` and an HBA port on `lab-b`.  Rename the host folders to your dock hosts, as ssh reaches them, and write one file per bay.

## Pushing it out

```
make hub-site                      # site/hub/ to the hub host, under your sudo
make bay-config bay HOST=lab-a     # check lab-a's bay configs, copy them, install the bundle
```

`bay-config` checks a host's configs with bayd's own parser before it copies anything: a typo, a name that doesn't match its file, or two bays that could reach one drive all stop it there.  It adds and updates configs but never deletes one.  Retiring a bay is deliberate (docs/operating.md).

## What never goes here

**Keys and certificates.**  They go from the CA straight to the host that uses them (docs/bringup.md §1, §3), and `deploy/ca.sh` refuses to write inside a git work tree, this one included.  A site folder you can share with a colleague, or commit, is one with nothing secret in it.
