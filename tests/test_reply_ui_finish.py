from types import SimpleNamespace
from browser_fixture import CamoufoxBrowserTests
from executor.platforms.claude.conversation import wait_for_reply_completion


class ReplyUiFinishTests(CamoufoxBrowserTests):
    def run_page(self, html, complete=True):
        context = self.browser.new_context(offline=True)
        self.addCleanup(context.close)
        page = context.new_page()
        page.set_content(html)
        observer = SimpleNamespace(failure=None, reply_completed=complete)
        return page, observer

    def test_stop_button_prevents_success_after_terminal(self):
        page, observer = self.run_page('<button>Stop response</button>')
        ok, _ = wait_for_reply_completion(page, completion=observer, timeout=.4)
        self.assertFalse(ok)

    def test_streaming_article_prevents_success(self):
        page, observer = self.run_page('<article aria-label="Currently streaming message">Reply</article>')
        ok, _ = wait_for_reply_completion(page, completion=observer, timeout=.4)
        self.assertFalse(ok)

    def test_status_prevents_success(self):
        page, observer = self.run_page('<div role="status">Claude is responding</div>')
        ok, _ = wait_for_reply_completion(page, completion=observer, timeout=.4)
        self.assertFalse(ok)

    def test_waits_for_same_page_transition(self):
        page, observer = self.run_page('''<button id="stop">Stop response</button>
            <script>setTimeout(() => {document.querySelector('#stop').remove();
            document.body.insertAdjacentHTML('beforeend','<p>UI finished</p>')}, 700)</script>''')
        ok, snapshot = wait_for_reply_completion(page, completion=observer, timeout=3)
        self.assertTrue(ok)
        self.assertIn('UI finished', snapshot)

    def test_ui_idle_without_terminal_is_not_success(self):
        page, observer = self.run_page('<p>UI finished</p>', complete=False)
        ok, _ = wait_for_reply_completion(page, completion=observer, timeout=.4)
        self.assertFalse(ok)

    def test_current_reply_ui_without_sse(self):
        html = '<article><h2>Claude responded: answer</h2><p>answer</p><button>Copy</button><button>Good response</button></article><div role="status">Claude finished the response</div>'
        page, observer = self.run_page(html, complete=False)
        ok, _ = wait_for_reply_completion(page, completion=observer, timeout=.5, baseline=0)
        self.assertTrue(ok)
        ok, _ = wait_for_reply_completion(page, completion=observer, timeout=.3, baseline=1)
        self.assertFalse(ok)

    def test_web_permission_continues_original_reply(self):
        html = """<article><h2>Claude responded: answer</h2><button>Copy</button><button>Good response</button></article>
        <div role="status">Claude finished the response</div>
        <div role="group" aria-label="Permission request: Web fetch"><dl><dt>Url</dt><dd>https://www.foxnews.com</dd></dl>
        <button onclick="this.parentElement.remove()">Allow once</button></div>"""
        page, observer = self.run_page(html, complete=False)
        ok, _ = wait_for_reply_completion(page, completion=observer, timeout=1, baseline=0)
        self.assertTrue(ok)

    def test_other_permission_blocks_even_terminal(self):
        page, observer = self.run_page('<div role="group" aria-label="Permission request: Delete"><button>Allow once</button></div>')
        ok, _ = wait_for_reply_completion(page, completion=observer, timeout=.4, baseline=0)
        self.assertFalse(ok)
