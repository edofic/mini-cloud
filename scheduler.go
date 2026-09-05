package main

import (
	"context"
	"log"
	"time"
)

func (a *App) syncCronLocked(cfg []CronConfig) {
	keep := map[string]bool{}
	for _, j := range cfg {
		keep[j.Name] = true
		if a.cron[j.Name] == nil {
			a.cron[j.Name] = &cronRun{running: map[*jobInvocation]struct{}{}, config: j}
		} else {
			a.cron[j.Name].config = j
		}
	}
	for n, r := range a.cron {
		if !keep[n] {
			for invocation := range r.running {
				invocation.cancel()
			}
			delete(a.cron, n)
		}
	}
}

func (g *Gateway) schedule(ctx context.Context) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			g.runDue(now)
		}
	}
}
func (g *Gateway) runDue(now time.Time) {
	g.mu.RLock()
	apps := make([]*App, 0, len(g.apps))
	for _, a := range g.apps {
		apps = append(apps, a)
	}
	g.mu.RUnlock()
	minute := now.Truncate(time.Minute)
	for _, a := range apps {
		a.mu.Lock()
		jobs := append([]CronConfig(nil), a.manifest.Cron...)
		for _, j := range jobs {
			expr, _ := parseCron(j.Schedule)
			run := a.cron[j.Name]
			if run == nil || run.last.Equal(minute) || !expr.matches(now) {
				continue
			}
			run.last = minute
			if len(run.running) > 0 {
				if j.Overlap == "replace" {
					for invocation := range run.running {
						invocation.cancel()
					}
				} else if j.Overlap != "allow" {
					run.lastResult = "skipped: previous invocation still running"
					continue
				}
			}
			invocationCtx, cancel := context.WithCancel(a.gw.ctx)
			invocation := &jobInvocation{ctx: invocationCtx, cancel: cancel, done: make(chan struct{})}
			run.running[invocation] = struct{}{}
			go a.runJob(j, run, invocation)
		}
		a.mu.Unlock()
	}
}
func (a *App) runJob(j CronConfig, run *cronRun, invocation *jobInvocation) {
	defer close(invocation.done)
	defer func() {
		a.mu.Lock()
		delete(run.running, invocation)
		a.mu.Unlock()
	}()
	timeout := j.Timeout.Duration
	if timeout <= 0 {
		timeout = time.Hour
	}
	ctx, cancel := context.WithTimeout(invocation.ctx, timeout)
	defer cancel()
	var files []string
	vars := map[string]string{}
	work := "."
	a.mu.Lock()
	if a.manifest.Process != nil {
		files = a.manifest.Process.EnvironmentFiles
		for k, v := range a.manifest.Process.Environment {
			vars[k] = v
		}
		work = a.manifest.Process.WorkingDirectory
	} else if a.manifest.CGI != nil {
		files = a.manifest.CGI.EnvironmentFiles
		for k, v := range a.manifest.CGI.Environment {
			vars[k] = v
		}
		work = a.manifest.CGI.WorkingDirectory
	}
	a.mu.Unlock()
	for k, v := range j.Environment {
		vars[k] = v
	}
	env, err := buildEnv(a.dir, work, files, vars, map[string]string{"APP_NAME": a.name, "CRON_JOB": j.Name})
	if err != nil {
		a.finishJob(run, "environment error: "+err.Error())
		return
	}
	args := interpolate(j.Command, env)
	cmd := appCommand(a.dir, work, args)
	cmd.Env = envList(env)
	cmd.Stdout = &logWriter{prefix: "app=" + a.name + " job=" + j.Name + " stream=stdout "}
	cmd.Stderr = &logWriter{prefix: "app=" + a.name + " job=" + j.Name + " stream=stderr "}
	if err = a.gw.startChild(ctx, cmd); err != nil {
		a.finishJob(run, "start error: "+err.Error())
		return
	}
	log.Printf("app=%s job=%s event=start", a.name, j.Name)
	err = a.gw.waitChild(cmd)
	result := "ok"
	if err != nil {
		result = err.Error()
	}
	a.finishJob(run, result)
	log.Printf("app=%s job=%s event=finish result=%q", a.name, j.Name, result)
}
func (a *App) finishJob(r *cronRun, result string) {
	a.mu.Lock()
	r.lastResult = result
	a.mu.Unlock()
}
