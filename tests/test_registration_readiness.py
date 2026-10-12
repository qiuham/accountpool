from browser_fixture import CamoufoxBrowserTests
from executor.platforms.claude.registration import wait_for_page_readiness


class RegistrationReadinessTests(CamoufoxBrowserTests):
    def show_page(self, path, html):
        self.context = self.browser.new_context()
        self.addCleanup(self.context.close)
        self.page = self.context.new_page()
        self.context.route('**/*', lambda route: route.fulfill(status=200, content_type='text/html', body=html))
        self.page.goto('https://fixture.invalid' + path)

    def test_authenticated_chat_is_ready_without_email_form(self):
        self.show_page('/new', '<title>Claude</title><textarea aria-label="Write your prompt to Claude"></textarea>')
        self.assertTrue(wait_for_page_readiness(self.page, timeout_s=0.5))

    def test_generic_controls_do_not_prove_login_or_chat_readiness(self):
        self.show_page('/login', '<title>Claude</title><input aria-label="Search"><button>Help</button>')
        self.assertFalse(wait_for_page_readiness(self.page, timeout_s=0.5))
