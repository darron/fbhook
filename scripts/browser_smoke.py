#!/usr/bin/env python3
"""Exercise real admin pages against web/browser_test.go's isolated fixtures."""
import json
import sys
from pathlib import Path
from playwright.sync_api import sync_playwright, expect

config = json.load(sys.stdin)
artifacts = Path(config['artifacts'])
artifacts.mkdir(parents=True, exist_ok=True)
with sync_playwright() as playwright:
    browser = playwright.chromium.launch(headless=True)
    context = browser.new_context(viewport={'width': 1440, 'height': 1000})
    context.add_cookies([{'name': 'fbhook-session', 'value': config['cookie'], 'url': config['url']}])
    page = context.new_page()
    failures = []
    page.on('pageerror', lambda error: failures.append(str(error)))
    page.goto(config['url'] + '/admin/endpoints')
    page.wait_for_load_state('networkidle')
    expect(page.get_by_role('heading', name='Feedback, connected.')).to_be_visible()
    page.screenshot(path=str(artifacts / 'endpoints-desktop.png'), full_page=True)
    page.get_by_role('button', name='+ New endpoint').click()
    page.get_by_label('Name', exact=True).fill('Browser-created endpoint')
    page.get_by_role('button', name='Create endpoint', exact=True).click()
    expect(page.get_by_role('heading', name='Your feedback URL')).to_be_visible()
    page.get_by_role('button', name='I’ve saved it').click()
    expect(page.get_by_role('heading', name='Browser-created endpoint')).to_be_visible()
    card = page.locator('article').filter(has=page.get_by_role('heading', name='Browser-created endpoint'))
    card.get_by_role('button', name='Pause', exact=True).click()
    expect(card.get_by_text('Paused', exact=True)).to_be_visible()
    card.get_by_role('button', name='Enable', exact=True).click()
    page.on('dialog', lambda dialog: dialog.accept())
    card.get_by_role('button', name='Rotate URL').click()
    expect(page.get_by_role('heading', name='Your feedback URL')).to_be_visible()
    page.get_by_role('button', name='I’ve saved it').click()
    page.get_by_role('link', name='Destinations', exact=False).first.click()
    page.wait_for_load_state('networkidle')
    page.get_by_role('button', name='+ New destination').click()
    page.get_by_label('Name', exact=True).fill('Browser destination')
    page.get_by_label('Repository (owner/repository)').fill('example/browser')
    page.get_by_label('Fine-grained GitHub token').fill('test-only')
    page.get_by_role('button', name='Validate & save').click()
    expect(page.get_by_role('heading', name='Browser destination')).to_be_visible()
    page.screenshot(path=str(artifacts / 'destinations-desktop.png'), full_page=True)
    page.get_by_role('link', name='Submissions', exact=False).click()
    page.wait_for_load_state('networkidle')
    page.screenshot(path=str(artifacts / 'submissions-desktop.png'), full_page=True)
    page.get_by_role('button').filter(has_text='Keep search filters').click()
    expect(page.get_by_role('dialog')).to_be_visible()
    expect(page.locator('pre')).to_contain_text('<script>window.injected=true</script>')
    expect(page.locator('pre')).to_contain_text('9007199254740993')
    expect(page.locator('pre')).to_contain_text('1e10000')
    assert page.evaluate('window.injected') is None, 'context executed as script'
    page.screenshot(path=str(artifacts / 'submission-detail.png'), full_page=True)
    page.keyboard.press('Escape')
    page.get_by_role('button').filter(has_text='An uncertain export response').click()
    page.get_by_label('Reason', exact=True).fill('Reviewed external state; abandon this fixture')
    page.get_by_role('button', name='Abandon delivery').click()
    expect(page.get_by_role('dialog').get_by_text('delivery abandoned by administrator')).to_be_visible()
    page.keyboard.press('Escape')
    response = context.request.post(config['url'] + '/api/v1/admin/endpoints', data={})
    assert response.status == 403, 'missing CSRF was accepted'
    page.set_viewport_size({'width': 390, 'height': 844})
    page.goto(config['url'] + '/admin/endpoints')
    page.wait_for_load_state('networkidle')
    assert page.evaluate('document.documentElement.scrollWidth <= window.innerWidth'), 'mobile overflow'
    page.screenshot(path=str(artifacts / 'endpoints-mobile.png'), full_page=True)
    context.clear_cookies()
    response = context.request.get(config['url'] + '/api/v1/admin/submissions')
    assert response.status == 401, 'anonymous access was accepted'
    assert not failures, failures
    browser.close()
print('Browser smoke passed: endpoint/destination CRUD, rotation, delivery resolution, safe context, CSRF, anonymous boundary, mobile layout.')
