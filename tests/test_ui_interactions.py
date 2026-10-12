import os
import unittest
from browser_fixture import CamoufoxBrowserTests
from executor.platforms.claude.registration import find_email_input, wait_for_page_readiness
from executor.platforms.claude.page_diagnostics import find_chat_input
from executor.platforms.claude.onboarding import check_required_onboarding_consents, click_onboarding_action

class UIInteractionTests(CamoufoxBrowserTests):


    def setUp(self):
        super().setUp()
        self.context = self.browser.new_context(offline=True)
        self.context.route('**/*', lambda route: route.abort())
        self.addCleanup(self.context.close)
        self.page = self.context.new_page()
        self.page.wait_for_timeout = lambda _: None

    def test_simple_form_is_ready(self):
        self.page.set_content('<input aria-label="Email"><button>Continue</button>')
        self.assertTrue(wait_for_page_readiness(self.page, timeout_s=0.2))

    def test_challenge_with_controls_is_not_ready(self):
        self.page.set_content('<div id="challenge-stage">Verify</div><input><button>Verify</button><a href="#">Help</a>')
        self.assertFalse(wait_for_page_readiness(self.page, timeout_s=0.2))

    def test_disabled_controls_are_not_ready(self):
        self.page.set_content('<input disabled><button disabled>Wait</button><a role="none">Text</a>')
        self.assertFalse(wait_for_page_readiness(self.page, timeout_s=0.2))

    def test_search_is_not_email(self):
        self.page.set_content('<input aria-label="Search settings">')
        self.assertIsNone(find_email_input(self.page))

    def test_email_type_without_label_is_found(self):
        self.page.set_content('<input aria-label="Search"><input id="email" type="email">')
        self.assertEqual(find_email_input(self.page).get_attribute('id'), 'email')

    def test_ambiguous_email_inputs_are_not_selected(self):
        self.page.set_content('<input aria-label="Email"><input aria-label="Email">')
        self.assertIsNone(find_email_input(self.page))

    def test_search_is_not_chat(self):
        self.page.set_content('<input aria-label="Search settings">')
        self.assertIsNone(find_chat_input(self.page))

    def test_chat_composer_is_found_after_search(self):
        self.page.set_content('<input aria-label="Search"><textarea id="composer" aria-label="Write your prompt"></textarea>')
        self.assertEqual(find_chat_input(self.page).get_attribute('id'), 'composer')

    def test_native_checkbox_with_aria_is_checked_once(self):
        self.page.set_content('<input id="one" type="checkbox" aria-label="I agree" role="checkbox" aria-checked="false">')
        self.assertEqual(check_required_onboarding_consents(self.page), 1)
        self.assertTrue(self.page.locator('#one').is_checked())
        self.assertEqual(check_required_onboarding_consents(self.page), 0)

    def test_optional_marketing_checkbox_is_left_unchanged(self):
        self.page.set_content('<input id="one" type="checkbox" aria-label="I agree"><input id="two" type="checkbox" aria-label="Subscribe to promotional emails">')
        self.assertEqual(check_required_onboarding_consents(self.page), 1)
        self.assertTrue(self.page.locator('#one').is_checked())
        self.assertFalse(self.page.locator('#two').is_checked())

    def test_custom_checkbox_changes_state_once(self):
        self.page.set_content('<button role="checkbox" aria-checked="false" onclick="this.setAttribute(\'aria-checked\', \'true\')">I accept</button>')
        self.assertEqual(check_required_onboarding_consents(self.page), 1)
        self.assertEqual(check_required_onboarding_consents(self.page), 0)

    def test_ambiguous_chat_inputs_are_not_selected(self):
        self.page.set_content('<textarea aria-label="Message"></textarea><textarea aria-label="Message"></textarea>')
        self.assertIsNone(find_chat_input(self.page))

    def test_hidden_email_does_not_make_visible_email_ambiguous(self):
        self.page.set_content('<input type="email" hidden><input id="email" type="email">')
        self.assertEqual(find_email_input(self.page).get_attribute('id'), 'email')

    def test_free_action_targets_button_not_plan_card(self):
        self.page.set_content('<div role="button" aria-label="Free Use Claude for free Other features" onclick="this.dataset.clicked=1"><button onclick="event.stopPropagation();this.dataset.clicked=1">Use Claude for free</button></div>')
        self.assertTrue(click_onboarding_action(self.page, "free"))
        self.assertEqual(self.page.locator('button').get_attribute('data-clicked'), '1')
        self.assertIsNone(self.page.locator('div').get_attribute('data-clicked'))

    def test_disabled_checkbox_is_left_unchanged(self):
        self.page.set_content('<input type="checkbox" disabled>')
        self.assertEqual(check_required_onboarding_consents(self.page), 0)
        self.assertFalse(self.page.locator('input').is_checked())

    def test_spanish_occupation_can_be_skipped(self):
        self.page.set_content('<select aria-label="Selecciona tu ocupación"><option>Elige</option></select><button onclick="this.dataset.clicked=1">Configurar más tarde</button>')
        self.assertTrue(click_onboarding_action(self.page, "skip"))
        self.assertEqual(self.page.locator('button').get_attribute('data-clicked'), '1')

    def test_owner_authorized_terms_and_age(self):
        self.page.set_content('<input id="consent" type="checkbox" aria-label="I agree to Anthropic Consumer Terms and confirm that I am at least 18 years of age."><input id="marketing" type="checkbox" aria-label="I agree to receive marketing emails">')
        self.assertEqual(check_required_onboarding_consents(self.page), 1)
        self.assertTrue(self.page.locator('#consent').is_checked())
        self.assertFalse(self.page.locator('#marketing').is_checked())
        self.assertEqual(check_required_onboarding_consents(self.page), 0)
