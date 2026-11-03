<script lang="ts">
  import { onMount, untrack } from 'svelte';
  import type { Bootstrap, Endpoint, Destination, Submission, SubmissionSummary, Status } from './lib/types';
  import { api, safeURL } from './lib/api';
  let { bootstrap }: { bootstrap:Bootstrap } = $props();
  const initial=untrack(()=>bootstrap);
  const page=initial.page;
  let dialogElement=$state<HTMLDialogElement>();
  $effect(()=>{if(selected&&dialogElement&&!dialogElement.open)dialogElement.showModal();});
  let endpoints=$state<Endpoint[]>(initial.data.endpoints);
  let destinations=$state<Destination[]>(initial.data.destinations);
  let submissions=$state<SubmissionSummary[]>(initial.data.submissions || []);
  let status=$state<Status>(initial.data.status);
  let endpointCursor=$state(initial.data.endpointCursor);
  let destinationCursor=$state(initial.data.destinationCursor);
  let submissionCursor=$state(initial.data.submissionCursor || '');
  let submissionPages=$state(1);
  let busy=$state(false), error=$state(''), notice=$state(''), secretURL=$state('');
  let showEndpoint=$state(false),showDestination=$state(false),selected=$state<Submission|null>(null);
  let endpointName=$state(''), destinationID=$state('');
  let destinationForm=$state({id:'',name:'',provider:'github',target:'',project:'',labels:'',token:''});
  let reason=$state(''),reference=$state(''),acknowledge=$state(false);
  const title=page==='/destinations'?'Give feedback a destination.':page==='/submissions'?'Every signal, accounted for.':'Feedback, connected.';
  const description=page==='/destinations'?'Configure where your applications’ feedback becomes work.':page==='/submissions'?'Inspect submissions, delivery attempts, and anything that needs a hand.':'One place for feedback from all your applications and agents.';
  const labels=(state:string)=>state.replaceAll('_',' ');
  const date=(value:string)=>new Date(value).toLocaleString(undefined,{month:'short',day:'numeric',hour:'2-digit',minute:'2-digit'});
  const destinationName=(id:string)=>destinations.find(d=>d.id===id)?.name || 'Destination';
  async function action(work:()=>Promise<void>){if(busy)return;busy=true;error='';notice='';try{await work();}catch(e){error=e instanceof Error?e.message:'Request failed';}finally{busy=false;}}
  async function refresh(refreshHistory=true){
    if(page==='/submissions'&&refreshHistory){
      const items:SubmissionSummary[]=[];
      let cursor='';
      for(let index=0;index<submissionPages;index++){
        const result=await api('submissions'+(cursor?'?cursor='+encodeURIComponent(cursor):''),bootstrap.csrfToken);
        items.push(...result.items);cursor=result.cursor;
        if(!cursor)break;
      }
      submissions=items;submissionCursor=cursor;
    }
    status=await api('status',bootstrap.csrfToken);
  }
  onMount(()=>{const timer=setInterval(()=>{
    if(busy||selected)return;
    busy=true;
    // Expanded history stays in place; explicit refresh reloads every loaded page.
    refresh(submissionPages===1).catch(()=>{error='Connection interrupted. Refresh to reconnect.';}).finally(()=>{busy=false;});
  },15000);return()=>clearInterval(timer);});
  function editDestination(d?:Destination){destinationForm=d?{id:d.id,name:d.name,provider:d.provider,target:d.target,project:d.project||'',labels:d.labels.join(', '),token:''}:{id:'',name:'',provider:'github',target:'',project:'',labels:'',token:''};showDestination=true;}
  async function saveDestination(){await action(async()=>{
    await api('destinations',bootstrap.csrfToken,{...destinationForm,labels:destinationForm.labels.split(',').map(v=>v.trim()).filter(Boolean)});
    destinationForm.token='';showDestination=false;const result=await api('destinations',bootstrap.csrfToken);destinations=result.items;destinationCursor=result.cursor;notice='Destination validated and saved.';
  });}
  async function createEndpoint(){await action(async()=>{
    const result=await api('endpoints',bootstrap.csrfToken,{name:endpointName,destination_id:destinationID});secretURL=result.url;showEndpoint=false;endpointName='';const list=await api('endpoints',bootstrap.csrfToken);endpoints=list.items;endpointCursor=list.cursor;
  });}
  async function rotate(ep:Endpoint){if(!confirm(`Rotate the URL for ${ep.name}? Applications using the old URL will need the new one.`))return;await action(async()=>{const result=await api(`endpoints/${ep.id}/rotate`,bootstrap.csrfToken,{});secretURL=result.url;});}
  async function toggle(ep:Endpoint){await action(async()=>{await api(`endpoints/${ep.id}`,bootstrap.csrfToken,{name:ep.name,destination_id:ep.destination_id,disabled:!ep.disabled});endpoints=endpoints.map(v=>v.id===ep.id?{...v,disabled:!v.disabled}:v);notice=ep.disabled?'Endpoint enabled.':'Endpoint paused.';});}
  async function credential(d:Destination,disabled:boolean){await action(async()=>{await api(`credentials/${d.credential_id}/status`,bootstrap.csrfToken,{disabled});notice=disabled?'Credential disabled. Pending deliveries will stop.':'Credential enabled.';});}
  async function loadMore(kind:'endpoints'|'destinations'|'submissions') {await action(async()=>{const cursor=kind==='endpoints'?endpointCursor:kind==='destinations'?destinationCursor:submissionCursor;const result=await api(`${kind}?cursor=${encodeURIComponent(cursor)}`,bootstrap.csrfToken);if(kind==='endpoints'){endpoints=[...endpoints,...result.items];endpointCursor=result.cursor;}else if(kind==='destinations'){destinations=[...destinations,...result.items];destinationCursor=result.cursor;}else{submissions=[...submissions,...result.items];submissionCursor=result.cursor;submissionPages++;}});}
  async function resolve(kind:string){if(!selected)return;const sub=selected;await action(async()=>{await api(`submissions/${sub.endpoint_id}/${sub.id}/resolve`,bootstrap.csrfToken,{action:kind,reason,reference,acknowledge_duplicate:acknowledge});selected=await api(`submissions/${sub.endpoint_id}/${sub.id}`,bootstrap.csrfToken);await refresh();notice='Delivery state updated.';});}
  async function openSubmission(sub:SubmissionSummary){await action(async()=>{const detail:Submission=await api(`submissions/${sub.endpoint_id}/${sub.id}`,bootstrap.csrfToken);selected=detail;reason='';reference=detail.reference?.id||'';acknowledge=false;});}
</script>

<div class="workspace">
  <aside class="sidebar">
    <a class="brand" href="/admin" aria-label="fbhook home"><span class="brand-symbol">↳</span>fbhook<span class="brand-dot">.</span></a>
    <div class="workspace-label">YOUR WORKSPACE <span>SELF-HOSTED</span></div>
    <nav aria-label="Main navigation">
      <a class:active={page==='/endpoints'} href="/admin/endpoints"><span>↳</span>Endpoints <small>{endpoints.length}</small></a>
      <a class:active={page==='/destinations'} href="/admin/destinations"><span>↗</span>Destinations <small>{destinations.length}</small></a>
      <a class:active={page==='/submissions'} href="/admin/submissions"><span>≡</span>Submissions</a>
    </nav>
    <div class="sidebar-note"><div class="signal-mark"><i></i><i></i><i></i><i></i></div><strong>A shorter path<br>from friction to fix.</strong><p>Your apps capture the context.<br>Your team decides what’s next.</p></div>
    <div class="account"><span class="avatar">{bootstrap.user.login.slice(0,1).toUpperCase()}</span><div><strong>{bootstrap.user.login}</strong><small>Administrator</small></div><form method="post" action="/auth/logout"><input type="hidden" name="csrf" value={bootstrap.csrfToken}><button class="icon-button" title="Sign out" aria-label="Sign out">↪</button></form></div>
  </aside>
  <main>
    <div class="topbar"><div class="breadcrumb">Workspace <span>/</span> {page.slice(1)}</div><div class="service-status"><span class:warn={!!status.last_error}></span>{status.last_error?'Worker needs attention':'Feedback service'}</div></div>
    <div class="content">
      <header class="page-header"><div><p class="eyebrow">{page==='/endpoints'?'CAPTURE':page==='/destinations'?'ROUTE':'DELIVER'}</p><h1>{title}</h1><p class="description">{description}</p></div><span class="header-mark" aria-hidden="true">{page==='/endpoints'?'↳':page==='/destinations'?'↗':'≡'}</span></header>
      {#if error}<div class="alert error" role="alert">{error}<button onclick={()=>error=''} aria-label="Dismiss error">×</button></div>{/if}
      {#if notice}<div class="alert success" role="status">{notice}<button onclick={()=>notice=''} aria-label="Dismiss notice">×</button></div>{/if}
      {#if secretURL}<section class="secret-panel" aria-label="New feedback URL"><div><p class="eyebrow">READY TO CONNECT</p><h2>Your feedback URL</h2><p>Save this URL in your application’s server configuration. It is shown only now.</p></div><code>{secretURL}</code><div class="button-row"><button class="primary" onclick={()=>action(async()=>{await navigator.clipboard.writeText(secretURL);notice='URL copied.';})}>Copy URL</button><button onclick={()=>secretURL=''}>I’ve saved it</button></div></section>{/if}
      {#if page==='/endpoints'}
        <section class="stats-strip"><div><span class="stat-number">{endpoints.filter(v=>!v.disabled).length}</span><div><strong>Active endpoints</strong><small>Ready to receive feedback</small></div></div><div><span class="stat-number">{destinations.length}</span><div><strong>Destinations</strong><small>Connected issue trackers</small></div></div><div><span class="stat-number accent">↗</span><div><strong>Many apps. One URL.</strong><small>Share an endpoint across applications</small></div></div></section>
        <div class="section-heading"><div><h2>Your endpoints <span>{endpoints.length}</span></h2><p>Each URL connects incoming feedback to a destination.</p></div><button class="primary" onclick={()=>{showEndpoint=!showEndpoint;destinationID=destinations[0]?.id||'';}} disabled={busy}>+ New endpoint</button></div>
        {#if showEndpoint}<form class="editor" onsubmit={e=>{e.preventDefault();createEndpoint();}}><div class="form-heading"><h3>Create an endpoint</h3><button type="button" class="icon-button" onclick={()=>showEndpoint=false} aria-label="Close endpoint form">×</button></div><div class="form-grid"><label>Name<input bind:value={endpointName} required maxlength="200" placeholder="Application feedback"></label><label>Destination<select bind:value={destinationID} required><option value="" disabled>Select a destination</option>{#each destinations as d}<option value={d.id}>{d.name}</option>{/each}</select></label></div>{#if !destinations.length}<p>Configure a <a href="/admin/destinations">destination</a> first.</p>{/if}<button class="primary" disabled={busy||!destinations.length}>{busy?'Creating…':'Create endpoint'}</button></form>{/if}
        {#if !endpoints.length}<div class="empty-state"><span class="empty-icon">↳</span><h3>Your first connection starts here.</h3><p>Connect a destination, create an endpoint, then send feedback<br>from any application or agent.</p><a href="/admin/destinations" class="text-link">Configure a destination ↗</a></div>{:else}<div class="endpoint-grid">{#each endpoints as ep}<article class="endpoint-card"><div class="card-top"><span class="endpoint-icon">↳</span><span class="badge" class:paused={ep.disabled}><i></i>{ep.disabled?'Paused':'Active'}</span></div><h3>{ep.name}</h3><code class="muted-code">{ep.id.slice(0,12)}…</code><div class="route"><span>DELIVERS TO</span><strong><span class="provider-icon">{destinations.find(d=>d.id===ep.destination_id)?.provider==='linear'?'◒':'⌘'}</span>{destinationName(ep.destination_id)}</strong></div><div class="card-actions"><button onclick={()=>rotate(ep)} disabled={busy}>Rotate URL</button><button onclick={()=>toggle(ep)} disabled={busy}>{ep.disabled?'Enable':'Pause'}</button></div></article>{/each}</div>{/if}
        {#if endpointCursor}<button class="load-more" onclick={()=>loadMore('endpoints')} disabled={busy}>Load more endpoints</button>{/if}
        <div class="integration-note"><span>⌁</span><div><strong>Built for applications. Easy for agents.</strong><p>Use the Go client or <code>fbhook send</code> to submit feedback with the context that matters.</p></div></div>
      {:else if page==='/destinations'}
        <div class="section-heading"><div><h2>Issue destinations <span>{destinations.length}</span></h2><p>Credentials stay here. Applications only need a feedback URL.</p></div><button class="primary" onclick={()=>editDestination()} disabled={busy}>+ New destination</button></div>
        {#if showDestination}<form class="editor" onsubmit={e=>{e.preventDefault();saveDestination();}}><div class="form-heading"><h3>{destinationForm.id?'Edit destination':'Connect an issue tracker'}</h3><button type="button" class="icon-button" onclick={()=>{showDestination=false;destinationForm.token='';}} aria-label="Close destination form">×</button></div><div class="form-grid"><label>Name<input bind:value={destinationForm.name} required maxlength="200" placeholder="Product feedback"></label><label>Provider<select bind:value={destinationForm.provider} disabled={!!destinationForm.id}><option value="github">GitHub</option><option value="linear">Linear</option></select></label><label>{destinationForm.provider==='github'?'Repository (owner/repository)':'Team UUID'}<input bind:value={destinationForm.target} required placeholder={destinationForm.provider==='github'?'owner/repository':'Team UUID'}></label>{#if destinationForm.provider==='linear'}<label>Project UUID (optional)<input bind:value={destinationForm.project}></label>{/if}<label>Labels, comma separated<input bind:value={destinationForm.labels} placeholder={destinationForm.provider==='github'?'feedback, agent':'Label UUIDs'}></label><label class="full">{destinationForm.provider==='github'?'Fine-grained GitHub token':'Linear API key'}<input type="password" bind:value={destinationForm.token} required={!destinationForm.id} autocomplete="new-password" placeholder={destinationForm.id?'Leave empty to keep current credential':'Stored encrypted; never shown again'}></label></div><p class="form-note">The target and labels are validated before saving. {destinationForm.id?'A new credential also applies to pending deliveries; their original targets stay captured.':''}</p><button class="primary" disabled={busy}>{busy?'Validating…':'Validate & save'}</button></form>{/if}
        {#if !destinations.length}<div class="empty-state"><span class="empty-icon">↗</span><h3>Choose where feedback lands.</h3><p>Connect a GitHub repository or a Linear team.<br>Endpoints will deliver issues there automatically.</p></div>{:else}<div class="destination-list">{#each destinations as d}<article class="destination-row"><span class="large-provider">{d.provider==='github'?'⌘':'◒'}</span><div class="destination-info"><h3>{d.name}</h3><p>{d.provider==='github'?'GitHub':'Linear'} <span>/</span> <code>{d.target}</code></p><div class="label-row">{#each d.labels as label}<span>{label}</span>{/each}</div></div><div class="button-row"><button onclick={()=>editDestination(d)}>Edit</button><button onclick={()=>credential(d,true)} disabled={busy}>Disable credential</button><button onclick={()=>credential(d,false)} disabled={busy}>Enable</button></div></article>{/each}</div>{/if}
        {#if destinationCursor}<button class="load-more" onclick={()=>loadMore('destinations')} disabled={busy}>Load more destinations</button>{/if}
      {:else}
        <section class="stats-strip"><div><span class="stat-number">{status.pending}</span><div><strong>Pending at last scan</strong><small>{status.last_scan.startsWith('0001')?'Waiting for first scan':date(status.last_scan)}</small></div></div><div><span class="stat-number accent">{status.uncertain}</span><div><strong>Need reconciliation</strong><small>Creation outcome needs confirmation</small></div></div><div><button onclick={()=>action(refresh)} disabled={busy}>↻ Refresh</button></div></section>
        <div class="section-heading"><div><h2>Submission history</h2><p>Stored feedback and the path it took to an issue.</p></div><span class="quiet">{submissionPages>1?'History stays in place. Refresh to update.':'Refreshes every 15 seconds'}</span></div>
        {#if !submissions.length}<div class="empty-state"><span class="empty-icon">≡</span><h3>Ready for the first signal.</h3><p>Feedback sent to an endpoint appears here<br>with its application context and delivery status.</p><a class="text-link" href="/admin/endpoints">View endpoints ↗</a></div>{:else}<div class="submission-list">{#each submissions as sub}<button class="submission-row" onclick={()=>openSubmission(sub)}><span class="submission-indicator" class:delivered={sub.state==='delivered'} class:attention={sub.state==='failed'||sub.state==='delivery_uncertain'}></span><span class="submission-title"><strong>{sub.feedback.title}</strong><small>{sub.feedback.source?.app||'Unspecified application'} <span>·</span> {sub.destination.name}</small></span><span class="state-label" class:attention={sub.state==='failed'||sub.state==='delivery_uncertain'}>{labels(sub.state)}</span><time>{date(sub.created_at)}</time><span>↗</span></button>{/each}</div>{/if}
        {#if submissionCursor}<button class="load-more" onclick={()=>loadMore('submissions')} disabled={busy}>Load more submissions</button>{/if}
      {/if}
      <footer><span>FBHOOK</span> Your feedback. Your infrastructure. Your workflow.</footer>
    </div>
  </main>
</div>
{#if selected}<dialog bind:this={dialogElement} class="detail-drawer" aria-label="Submission details" onclose={()=>selected=null}><div class="form-heading"><p class="eyebrow">SUBMISSION DETAIL</p><button class="icon-button" onclick={()=>selected=null} aria-label="Close submission details">×</button></div><h2>{selected.feedback.title}</h2><div class="detail-meta"><span class="state-label">{labels(selected.state)}</span><span>{date(selected.created_at)}</span><span>{selected.total_attempts} attempts</span></div><p class="feedback-text">{selected.feedback.feedback}</p><h3>Source & context</h3><pre>{selected.context_json}</pre>{#if selected.reference}<a class="text-link" href={safeURL(selected.reference.url)} target="_blank" rel="noreferrer">Open issue ↗</a>{/if}{#if selected.error}<p class="alert error">{selected.error}</p>{/if}<h3>Delivery history</h3><ol class="history">{#each selected.history as item}<li><strong>{labels(item.to)}</strong><time>{date(item.at)}</time>{#if item.reason}<p>{item.reason}</p>{/if}{#if item.actor}<small>Actor: {item.actor}</small>{/if}</li>{/each}</ol>{#if selected.state!=='delivered'&&selected.state!=='delivering'}<form class="resolution" onsubmit={e=>{e.preventDefault();resolve('retry');}}><h3>Resolve delivery</h3><label>Reason<textarea bind:value={reason} required maxlength="1000" placeholder="What did you check or change?"></textarea></label>{#if selected.creation_uncertain}<label class="checkbox"><input type="checkbox" bind:checked={acknowledge}>I understand retrying may create a duplicate issue.</label>{/if}{#if selected.reference}<p>An issue already exists. Correct its labels upstream, then verify it here.</p>{/if}{#if selected.creation_uncertain||selected.reference}<label>Existing issue ID<input bind:value={reference} placeholder="GitHub issue number or Linear issue ID"></label><button type="button" onclick={()=>resolve('link')} disabled={busy||!reason||!reference}>Verify & link existing issue</button>{/if}<div class="button-row"><button class="primary" disabled={busy||!reason||selected.state==='pending'||!!selected.reference}>Retry delivery</button><button type="button" onclick={()=>resolve('abandon')} disabled={busy||!reason}>Abandon delivery</button></div></form>{/if}</dialog>{/if}
